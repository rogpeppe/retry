// Package retry implements an efficientloop-based retry mechanism
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
	"sync"
	"time"
)

// Strategy represents a retry strategy. This specifies how a set of retries should
// be made and can be reused for any number of attempts (it's treated as immutable
// by this package).
//
// If an iteration takes longer than the delay for that iteration, the next
// iteration will be moved accordingly. For example, if the strategy
// has a delay of 1ms and the first two tries take 1s and 0.5ms respectively,
// then the second try will start immediately after the first (at 1s), but
// the third will start at 1.1s.
type Strategy struct {
	// Delay holds the amount of time between starting each retry
	// attempt. If Factor is greater than 1 or MaxDelay is greater
	// than Delay, then the maximum delay time will increase
	// exponentially (modulo jitter) as attempts continue, up to a
	// maximum of MaxDuration if that's non-zero.
	Delay time.Duration

	// MaxDelay holds the maximum amount of time between
	// starting each retry attempt. If this is greater than Delay,
	// the strategy is exponential - the time between attempts
	// will multiply by Factor on each attempt.
	MaxDelay time.Duration

	// Factor holds the exponential factor used when calculating the
	// next retry attempt. If the strategy is exponential (MaxDelay > Delay),
	// and Factor is <= 1, it will be treated as 2.
	//
	// If the initial delay is 0, it will be treated as 1ns before multiplying
	// for the first time, avoiding an infinite loop.
	//
	// The actual delay will be randomized by applying jitter
	// according to the "Full Jitter" algorithm described in
	// https://aws.amazon.com/blogs/architecture/exponential-backoff-and-jitter/
	Factor float64

	// MaxCount limits the total number of attempts that will be made. If it's zero,
	// the number of attempts is unlimited.
	MaxCount int

	// MaxDuration limits the total amount of time taken by all attempts.
	// An attempt will not be started if the time since Start was called
	// exceeds MaxDuration.
	// If MaxDuration is <= 0, there is no time limit.
	MaxDuration time.Duration
}

// jitter is used by tests to disable jitter.
var jitter = true

// TODO implement ParseStrategy to parse a string like:
// 	1ms..30s*2; 20; 2m
// minDelay [ ".." maxDelay [ "*" factor] ] [ ";" [ maxCount ] [ ";" [ maxDuration ] ]

// Start starts a set of retry attempts using s as
// a retry strategy and returns an Iter
// that can be used to wait for each attempt in turn.
//
// Note that an Attempt can be used for multiple
// sets of retries (but not concurrently).
func (s *Strategy) Start(stop <-chan struct{}) *Iter {
	var a Iter
	a.Start(s, stop, nil)
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
	// start holds when the current attempts started.
	start time.Time
	// tryStart holds the time that the next attempt should start.
	tryStart time.Time
	// delay holds the current delay between attempt starts.
	// (only used if the strategy is exponential)
	delay time.Duration
	// count holds the number of attempts made.
	count int
	now   func() time.Time
	stop  <-chan struct{}
	timer *time.Timer
}

// Start is like Strategy.Start but initializes an existing Iter
// value which can save an allocation.
//
// It also accepts a function that is used to get the current time.
// If that's nil, time.Now will be used.
func (i *Iter) Start(strategy *Strategy, stop <-chan struct{}, now func() time.Time) {
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
	// We'll always make at least one attempt.
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

// Next sleeps until the next attempt is to be made and
// reports whether there are any more attempts remaining.
func (i *Iter) Next() bool {
	t, ok := i.NextTime()
	if !ok {
		return false
	}
	if !i.sleep(t.Sub(i.now())) {
		i.stopped = true
		i.inProgress = false
		return false
	}
	return true
}

// NextTime is similar to Next except that it instead returns
// immediately with the time that the next attempt should be made.
// The caller is responsible for actually waiting.
//
// This can return a time in the past if the previous attempt
// lasted longer than its available timeslot.
func (i *Iter) NextTime() (time.Time, bool) {
	if !i.HasMore() {
		return time.Time{}, false
	}
	i.count++
	i.hasMoreCalled = false
	return i.tryStart, true
}

// HasMore returns immediately and reports whether there are more retry
// attempts left. If it returns true, then Next will return true unless
// a value was received on the stop channel, in which case a.WasStopped
// will return true.
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
	if !i.strategy.isExponential() {
		i.tryStart = i.tryStart.Add(i.strategy.Delay)
	} else {
		actualDelay := i.delay
		if jitter {
			actualDelay = randDuration(actualDelay)
		}
		i.tryStart = i.tryStart.Add(actualDelay)
		i.delay = time.Duration(float64(i.delay) * i.strategy.Factor)
		if i.delay > i.strategy.MaxDelay {
			i.delay = i.strategy.MaxDelay
		}
	}
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

// Count returns the number of iterations so far, starting at 1.
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
