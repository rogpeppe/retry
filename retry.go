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
type Strategy struct {
	// Delay holds the amount of time between starting each retry attempt.
	// If Factor is greater than 1 or MaxDelay is greater than Delay,
	// then the delay time will increase exponentially (modulo jitter) as attempts continue,
	// up to a maximum of MaxDuration if that's non-zero.
	Delay time.Duration

	// MaxDelay holds the maximum amount of time between
	// starting each retry attempt. If this is greater than Delay,
	// the strategy is exponential - the time between attempts
	// will multiply by Factor on each attempt (but see NoJitter).
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
// a retry strategy and returns an Attempt
// that can be used to wait for each attempt in turn.
//
// Attempts will
//
// Note that an Attempt can be used for multiple
// sets of retries (but not concurrently).
func (s *Strategy) Start(stop <-chan struct{}) *Attempt {
	var a Attempt
	a.Start(s, stop, nil)
	return &a
}

func (s *Strategy) isExponential() bool {
	return s.MaxDelay > s.Delay || s.Factor > 0
}

// Attempt represents a particular retry attempt using some retry strategy.
type Attempt struct {
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

// Start is like Strategy.Start but initializes an existing Attempt
// value, saving an allocation.
//
// It also accepts a function that is used to get the current time.
// If that's nil, time.Now will be used.
func (a *Attempt) Start(strategy *Strategy, stop <-chan struct{}, now func() time.Time) {
	a.strategy = *strategy
	if a.strategy.isExponential() {
		if a.strategy.Factor <= 1 {
			a.strategy.Factor = 2
		}
		if a.strategy.Delay <= 0 {
			a.strategy.Delay = 1
		}
		if a.strategy.MaxDelay <= 0 {
			a.strategy.MaxDelay = math.MaxInt64
		}
	}
	if a.strategy.MaxCount == 0 {
		a.strategy.MaxCount = math.MaxInt
	}
	if now == nil {
		now = time.Now
	}
	a.now = now
	a.start = now()
	a.tryStart = a.start
	a.delay = a.strategy.Delay
	// We'll always make at least one attempt.
	a.hasMoreCalled = true
	a.inProgress = true
	a.count = 1
	a.stop = stop
}

// WasStopped reports whether the attempt was stopped
// because a value was received on the stop channel.
func (a *Attempt) WasStopped() bool {
	return a.stopped
}

// Next sleeps until the next attempt is to be made and
// reports whether there are any more attempts remaining.
func (a *Attempt) Next() bool {
	t, ok := a.NextTime()
	if !ok {
		return false
	}
	if !a.sleep(t.Sub(a.now())) {
		a.stopped = true
		a.inProgress = false
		return false
	}
	return true
}

// NextTime is similar to Next except that it instead returns
// immediately with time that the next attempt should be made at.
// The caller is responsible for actually waiting.
//
// This can return a time in the past if the previous attempt
// lasted longer than its available timeslot.
func (a *Attempt) NextTime() (time.Time, bool) {
	if !a.HasMore() {
		return time.Time{}, false
	}
	a.hasMoreCalled = false
	return a.tryStart, true
}

// HasMore returns immediately and  whether there are more retry
// attempts left. If it returns true, then Next will return true
// unless a value was received on the stop channel, in which case
// a.WasStopped will return true.
func (a *Attempt) HasMore() bool {
	if a.hasMoreCalled {
		return a.inProgress
	}
	a.inProgress = a.updateNext()
	a.hasMoreCalled = true
	return a.inProgress
}

func (a *Attempt) updateNext() bool {
	if a.count >= a.strategy.MaxCount {
		return false
	}
	if !a.strategy.isExponential() {
		a.tryStart = a.tryStart.Add(a.strategy.Delay)
	} else {
		actualDelay := a.delay
		if jitter {
			actualDelay = randDuration(actualDelay)
		}
		a.tryStart = a.tryStart.Add(actualDelay)
		a.delay = time.Duration(float64(a.delay) * a.strategy.Factor)
		if a.delay > a.strategy.MaxDelay {
			a.delay = a.strategy.MaxDelay
		}
	}
	if a.strategy.MaxDuration != 0 {
		now := a.now()
		if now.Sub(a.start) > a.strategy.MaxDuration || a.tryStart.Sub(a.start) > a.strategy.MaxDuration {
			return false
		}
	}
	a.count++
	return true
}

func (a *Attempt) Count() int {
	return a.count
}

func (a *Attempt) sleep(d time.Duration) bool {
	if a.stop == nil {
		time.Sleep(d)
		return true
	}
	if d <= 0 {
		// We're not going to sleep for any time, so make sure
		// we respect the stop channel.
		select {
		case <-a.stop:
			return false
		default:
			return true
		}
	}
	if a.timer == nil {
		a.timer = time.NewTimer(d)
	} else {
		a.timer.Reset(d)
	}
	select {
	case <-a.stop:
		// Stop the timer to be sure we can continue to use the timer
		// if the Attempt is reused.
		if !a.timer.Stop() {
			<-a.timer.C
		}
		return false
	case <-a.timer.C:
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
