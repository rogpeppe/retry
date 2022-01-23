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
	"sync"
	"time"
)

// Iter represents a particular retry iteration loop using some strategy.
type Iter struct {
	strategy Strategy
	// start holds when the current loop started.
	start time.Time
	// tryStart holds the time that the next iteration should start.
	tryStart time.Time
	// delay holds the current delay between iterations.
	// (only used if the strategy is exponential)
	delay time.Duration
	// count holds the number of iterations so far.
	count      int
	now        func() time.Time
	timer      *time.Timer
	stopped    bool
	inProgress bool
}

// Start starts a retry loop using s as a retry strategy and
// returns an Iter that can be used to wait for each retry
// in turn. Note: the first try should be made immediately
// after calling Start without calling Next.
func (s *Strategy) Start() *Iter {
	var a Iter
	a.Reset(s, nil)
	return &a
}

// Reset is like Strategy.Start but initializes an existing Iter
// value which can save the allocation of the underlying
// time.Timer used when Next is called with a non-nil stop channel.
//
// It also accepts a function that is used to get the current time.
// If that's nil, time.Now will be used.
//
// It's OK to call this on the zero Iter value.
func (i *Iter) Reset(strategy *Strategy, now func() time.Time) {
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
	i.delay = i.strategy.Delay
	i.tryStart = i.start
	i.stopped = false
	i.inProgress = true
	i.count = 1
}

// WasStopped reports whether the iteration was stopped
// because a value was received on a stop channel passed to Next
func (i *Iter) WasStopped() bool {
	return i.stopped
}

// Next sleeps until the next iteration is to be made and
// reports whether there are any more iterations remaining.
//
// If a value is received on the stop channel, it immediately
// stops waiting for the next iteration and returns false.
func (i *Iter) Next(stop <-chan struct{}) bool {
	t, ok := i.nextTime()
	if !ok {
		return false
	}
	if !i.sleep(stop, t.Sub(i.now())) {
		i.stopped = true
		return false
	}
	i.count++
	return true
}

// NextTime is similar to Next except that it instead returns
// immediately with the time that the next iteration should begin.
// The caller is responsible for actually waiting until that time.
func (i *Iter) NextTime() (time.Time, bool) {
	t, ok := i.nextTime()
	if ok {
		i.count++
	}
	return t, ok
}

// TryTime returns the time that the current try iteration should be
// made at, if there should be one. If iteration has finished, it
// returns (time.Time{}, false).
//
// The returned time can be in the past (after Start or Reset or Next
// have been called) or in the future (after NextTime has been called,
// TryTime returns the same values that NextTime returned).
//
// Calling TryTime repeatedly will return the same values until
// Next or NextTime or Reset have been called.
func (i *Iter) TryTime() (time.Time, bool) {
	return i.tryStart, i.inProgress
}

// StartTime returns the time that the
// iterator was created or last reset.
func (i *Iter) StartTime() time.Time {
	return i.start
}

func (i *Iter) nextTime() (time.Time, bool) {
	if i.updateNext() {
		return i.tryStart, true
	}
	i.inProgress = false
	return time.Time{}, false
}

func (i *Iter) updateNext() bool {
	if i.count >= i.strategy.MaxCount {
		return false
	}
	var actualDelay time.Duration
	if i.strategy.isExponential() {
		actualDelay = i.delay
		i.delay = time.Duration(float64(i.delay) * i.strategy.Factor)
		if i.delay > i.strategy.MaxDelay {
			i.delay = i.strategy.MaxDelay
		}
	} else {
		actualDelay = i.strategy.Delay
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

func (i *Iter) sleep(stop <-chan struct{}, d time.Duration) bool {
	if stop == nil {
		time.Sleep(d)
		return true
	}
	if d <= 0 {
		// We're not going to sleep for any time, so make sure
		// we respect the stop channel.
		select {
		case <-stop:
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
	case <-stop:
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
