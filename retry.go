// Package retry implements an efficient loop-based retry mechanism
// that allows the retry policy to be specified independently of the control structure.
// It supports exponential (with jitter) and linear retry policies.
//
// Although syntactically lightweight, it's also flexible - for example,
// it can be used to run a backoff loop while waiting for other concurrent
// events, or with mocked-out time.
package retry

import (
	"math"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Strategy represents a retry strategy. This specifies how a set of retries should
// be made and can be reused for any number of loops (it's treated as immutable
// by this package).
//
// If an iteration takes longer than the delay for that iteration, the next
// iteration will be moved accordingly. For example, if the strategy
// has a delay of 1ms and the first two tries take 1s and 0.5ms respectively,
// then the second try will start immediately after the first (at 1s), but
// the third will start at 1.1s.
//
// All strategies will loop for at least one iteration. The only time a loop
// might terminate immediately is when a value is received on
// the stop channel.
type Strategy struct {
	// Delay holds the amount of time between the start of each iteration.
	// If Factor is greater than 1 or MaxDelay is greater
	// than Delay, then the maximum delay time will increase
	// exponentially (modulo jitter) as iterations continue, up to a
	// maximum of MaxDuration if that's non-zero.
	Delay time.Duration

	// MaxDelay holds the maximum amount of time between
	// the start of each iteration. If this is greater than Delay,
	// the strategy is exponential - the time between iterations
	// will multiply by Factor on each iteration.
	MaxDelay time.Duration

	// Factor holds the exponential factor used when calculating the
	// next iteration delay. If the strategy is exponential (MaxDelay > Delay),
	// and Factor is <= 1, it will be treated as 2.
	//
	// If the initial delay is 0, it will be treated as 1ns before multiplying
	// for the first time, avoiding an infinite loop.
	//
	// The actual delay will be randomized by applying jitter
	// according to the "Full Jitter" algorithm described in
	// https://aws.amazon.com/blogs/architecture/exponential-backoff-and-jitter/
	Factor float64

	// Regular specifies that the backoff timing should be adhered
	// to as closely as possible with no jitter. When this is false,
	// the actual delay between iterations will be chosen randomly
	// from the interval (0, d] where d is the delay for that iteration.
	Regular bool

	// MaxCount limits the total number of iterations. If it's zero,
	// the count is unlimited.
	MaxCount int

	// MaxDuration limits the total amount of time taken by the
	// whole loop. An iteration will not be started if the time
	// since Start was called exceeds MaxDuration. If MaxDuration <= 0,
	// there is no time limit.
	MaxDuration time.Duration
}

// String returns the strategy in the format:
//
// 	~1ms**1.5..30s; 20; 2m
//
// The grammar is:
//
// 	[ "~" ] minDelay [ [ "**" factor ] ".." maxDelay ] [ "; " [ maxCount ] [ "; " [ maxDuration ] ]
//
// TODO implement ParseStrategy to parse the above grammar.
func (s *Strategy) String() string {
	var buf strings.Builder
	if !s.Regular {
		buf.WriteByte('~')
	}
	buf.WriteString(s.Delay.String())
	if s.MaxDelay > s.Delay {
		factor := s.Factor
		if factor <= 1 {
			factor = 2
		}
		if factor != 2 {
			buf.WriteString("**")
			buf.WriteString(strconv.FormatFloat(factor, 'g', -1, 64))
		}
		buf.WriteString("..")
		buf.WriteString(s.MaxDelay.String())
	}
	if s.MaxCount > 0 || s.MaxDuration > 0 {
		buf.WriteString("; ")
		if s.MaxCount > 0 {
			buf.WriteString(strconv.FormatInt(int64(s.MaxCount), 10))
		}
		if s.MaxDuration > 0 {
			buf.WriteString("; ")
			buf.WriteString(s.MaxDuration.String())
		}
	}
	return buf.String()
}

// Start starts a retry loop using s as a retry strategy and
// returns an Iter that can be used to wait for each iteration in turn
// and is terminated if a value is received on the stop channel.
//
// Note that in general, there will always be at least one iteration
// regardless of the strategy, but regardless of that, the iteration
// will terminate immediately if a value is received on the stop channel
func (s *Strategy) Start(stop <-chan struct{}) *Iter {
	var a Iter
	a.Reset(s, stop, nil)
	return &a
}

func (s *Strategy) isExponential() bool {
	return s.MaxDelay > s.Delay || s.Factor > 0
}

// Iter represents a particular retry iteration loop using some strategy.
type Iter struct {
	hasMoreCalled bool
	stopped       bool
	inProgress    bool
	strategy      Strategy
	// start holds when the current loop started.
	start time.Time
	// tryStart holds the time that the next iteration should start.
	tryStart time.Time
	// delay holds the current delay between iterations.
	// (only used if the strategy is exponential)
	delay time.Duration
	// count holds the number of iterations so far.
	count int
	now   func() time.Time
	stop  <-chan struct{}
	timer *time.Timer
}

// Reset is like Strategy.Start but initializes an existing Iter
// value which can save the allocation of the underlying
// time.Timer used when the stop channel is non-nil.
//
// It also accepts a function that is used to get the current time.
// If that's nil, time.Now will be used.
//
// It's OK to call this on the zero Iter value.
func (i *Iter) Reset(strategy *Strategy, stop <-chan struct{}, now func() time.Time) {
	i.strategy = *strategy
	if i.strategy.isExponential() {
		if i.strategy.Factor <= 1 {
			i.strategy.Factor = 2
		}
		if i.strategy.Delay <= 0 {
			i.strategy.Delay = 1
		}
		if i.strategy.MaxDelay <= 0 {
			i.strategy.MaxDelay = math.MaxInt64
		}
	}
	if i.strategy.MaxCount == 0 {
		i.strategy.MaxCount = math.MaxInt
	}
	if now == nil {
		now = time.Now
	}
	i.now = now
	i.start = now()
	i.tryStart = i.start
	i.delay = i.strategy.Delay
	// We always want at least one iteration.
	i.hasMoreCalled = true
	i.inProgress = true
	i.count = 0
	i.stop = stop
}

// WasStopped reports whether the iteration was stopped
// because a value was received on the stop channel.
func (i *Iter) WasStopped() bool {
	return i.stopped
}

// Next sleeps until the next iteration is to be made and
// reports whether there are any more iterations remaining.
func (i *Iter) Next() bool {
	t, ok := i.nextTime()
	if !ok {
		return false
	}
	if !i.sleep(t.Sub(i.now())) {
		i.stopped = true
		i.inProgress = false
		return false
	}
	i.count++
	return true
}

// NextTime is similar to Next except that it instead returns
// immediately with the time that the next iteration should begin.
// The caller is responsible for actually waiting, and the stop
// channel is ignored.
func (i *Iter) NextTime() (time.Time, bool) {
	t, ok := i.nextTime()
	if ok {
		i.count++
	}
	return t, ok
}

func (i *Iter) nextTime() (time.Time, bool) {
	if !i.HasMore() {
		return time.Time{}, false
	}
	i.hasMoreCalled = false
	return i.tryStart, true
}

// HasMore returns immediately and reports whether there are more
// iterations remaining. If it returns true, then Next will return true unless
// a value was received on the stop channel, in which case it will return false
// and a.WasStopped will return true.
func (i *Iter) HasMore() bool {
	if i.hasMoreCalled {
		return i.inProgress
	}
	i.inProgress = i.updateNext()
	i.hasMoreCalled = true
	return i.inProgress
}

func (i *Iter) updateNext() bool {
	if i.count >= i.strategy.MaxCount {
		return false
	}
	var actualDelay time.Duration
	if !i.strategy.isExponential() {
		actualDelay = i.strategy.Delay
	} else {
		actualDelay = i.delay
		i.delay = time.Duration(float64(i.delay) * i.strategy.Factor)
		if i.delay > i.strategy.MaxDelay {
			i.delay = i.strategy.MaxDelay
		}
	}
	if !i.strategy.Regular {
		actualDelay = randDuration(actualDelay)
	}
	i.tryStart = i.tryStart.Add(actualDelay)
	now := i.now()
	if i.tryStart.Before(now) {
		i.tryStart = now
	}
	if i.strategy.MaxDuration != 0 {
		if now.Sub(i.start) > i.strategy.MaxDuration || i.tryStart.Sub(i.start) > i.strategy.MaxDuration {
			return false
		}
	}
	return true
}

// Count returns the number of iterations so far. Specifically,
// this returns the number of times that Next or NextTime have returned true.
func (i *Iter) Count() int {
	return i.count
}

func (i *Iter) sleep(d time.Duration) bool {
	if i.stop == nil {
		time.Sleep(d)
		return true
	}
	if d <= 0 {
		// We're not going to sleep for any time, so make sure
		// we respect the stop channel.
		select {
		case <-i.stop:
			return false
		default:
			return true
		}
	}
	if i.timer == nil {
		i.timer = time.NewTimer(d)
	} else {
		i.timer.Reset(d)
	}
	select {
	case <-i.stop:
		// Stop the timer to be sure we can continue to use the timer
		// if the Iter is reused.
		if !i.timer.Stop() {
			<-i.timer.C
		}
		return false
	case <-i.timer.C:
		return true
	}
}

var (
	// randomMu guards random.
	randomMu sync.Mutex
	// random is used as a random number source for jitter.
	// We avoid using the global math/rand source
	// as we don't want to be responsible for seeding it,
	// and its lock may be more contended.
	random = rand.New(rand.NewSource(time.Now().UnixNano()))
)

// randDuration returns a random duration between 0 and max-1.
func randDuration(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	randomMu.Lock()
	defer randomMu.Unlock()
	return time.Duration(random.Int63n(int64(max)))
}
