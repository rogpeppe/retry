package retry

import (
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func TestAttemptTiming(t *testing.T) {
	c := qt.New(t)
	testAttempt := Strategy{
		Delay:       0.1e9,
		MaxDuration: 0.25e9,
	}
	want := []time.Duration{0, 0.1e9, 0.2e9, 0.2e9}
	got := make([]time.Duration, 0, len(want)) // avoid allocation when testing timing
	t0 := time.Now()
	a := testAttempt.Start(nil)
	for a.Next() {
		got = append(got, time.Now().Sub(t0))
	}
	got = append(got, time.Now().Sub(t0))
	c.Assert(a.WasStopped(), qt.Equals, false)
	c.Assert(got, qt.HasLen, len(want))
	const margin = 0.01e9
	for i, got := range want {
		lo := want[i] - margin
		hi := want[i] + margin
		if got < lo || got > hi {
			c.Errorf("attempt %d want %g got %g", i, want[i].Seconds(), got.Seconds())
		}
	}
}

func TestAttemptWithStop(t *testing.T) {
	c := qt.New(t)
	stop := make(chan struct{})
	close(stop)
	done := make(chan struct{})
	go func() {
		strategy := Strategy{
			Delay:       5 * time.Second,
			MaxDuration: 30 * time.Second,
		}
		a := strategy.Start(stop)
		for a.Next() {
			c.Errorf("unexpected attempt")
		}
		c.Check(a.WasStopped(), qt.Equals, true)
		close(done)
	}()
	assertReceive(c, done, "attempt loop abort")
}

type strategyTest struct {
	testName   string
	strategy   Strategy
	calls      []nextCall
	terminates bool
}

type nextCall struct {
	// t holds the time since the timer was started that
	// the Next call will be made.
	t time.Duration
	// delay holds the length of time that a call made at
	// time t is expected to sleep for.
	sleep time.Duration
}

var strategyTests = []strategyTest{{
	testName: "Regular",
	strategy: Strategy{
		MaxDuration: 0.25e9,
		Delay:       0.1e9,
	},
	calls: []nextCall{
		{0, 0},
		{0, 0.1e9},
		{0.1e9, 0.1e9},
		{0.2e9, 0},
	},
	terminates: true,
}, {
	testName: "RegularWithCallsAtDifferentTimes",
	strategy: Strategy{
		Delay:       1e9,
		MaxDuration: 2.5e9,
	},
	calls: []nextCall{
		{0.5e9, 0},
		{0.5e9, 0.5e9},
		{1.1e9, 0.9e9},
		{2.2e9, 0},
	},
	terminates: true,
}, {
	testName: "RegularWithCallAfterNextDeadline",
	strategy: Strategy{
		MaxDuration: 3.5e9,
		Delay:       1e9,
	},
	calls: []nextCall{
		{0.5e9, 0},
		// We call Next at well beyond the deadline,
		// so we get a zero delay, but subsequent events
		// resume pace.
		{2e9, 0},
		{2.1e9, 0},
		{3e9, 0},
		{4e9, 0},
	},
	terminates: true,
}, {
	testName: "Exponential",
	strategy: Strategy{
		Delay:  1e9,
		Factor: 2,
	},
	calls: []nextCall{
		{0, 0},
		{0.1e9, 0.9e9},
		{1e9, 2e9},
		{3e9, 4e9},
		{7e9, 8e9},
	},
}, {
	testName: "ExponentialWithSmallFactor",
	strategy: Strategy{
		Delay:  1e9,
		Factor: 0.9,
	},
	calls: []nextCall{
		{0, 0},
		{0.1e9, 0.9e9},
		{1e9, 2e9},
		{3e9, 4e9},
		{7e9, 8e9},
	},
}, {
	testName: "ExponentialMaxTime",
	strategy: Strategy{
		Delay:       time.Second,
		Factor:      2,
		MaxDuration: 5 * time.Second,
	},
	calls: []nextCall{
		{0, 0},
		{0.1e9, 0.9e9},
		{1e9, 2e9},
		{3e9, 0},
	},
	terminates: true,
}, {
	testName: "ExponentialMaxCount",
	strategy: Strategy{
		Delay:    time.Second,
		MaxCount: 2,
		Factor:   2,
	},
	calls: []nextCall{
		{0, 0},
		{0.1e9, 0.9e9},
		{1e9, 0},
	},
	terminates: true,
}}

func TestStrategies(t *testing.T) {
	c := qt.New(t)
	for _, test := range strategyTests {
		c.Run(test.testName, func(c *qt.C) {
			noJitter(t)
			t0 := time.Now()
			now := t0

			var a Attempt
			a.Start(&test.strategy, nil, func() time.Time {
				return now
			})
			for i, call := range test.calls {
				now = t0.Add(call.t)
				nextt, ok := a.NextTime()
				expectTerminate := test.terminates && i == len(test.calls)-1
				c.Assert(ok, qt.Equals, !expectTerminate)
				if ok {
					c.Logf("call %d at %v - got %v want %v", i, now.Sub(t0), nextt.Sub(t0), call.t)
					if nextt.After(now) {
						now = nextt
					}
				} else {
					c.Logf("call %d - got nothing want %v", i, call.t)
					if !nextt.IsZero() {
						c.Fatalf("NextTime should return (time.Time{}, false)")
					}
				}
				// Allow for vagaries of floating point arithmetic.
				c.Check(now.Sub(t0), qt.CmpEquals(cmpopts.EquateApproxTime(20*time.Nanosecond)), call.t+call.sleep)
				if ok {
					c.Assert(a.Count(), qt.Equals, i+1)
				}
			}
		})
	}
}

func TestExponentialWithJitter(t *testing.T) {
	c := qt.New(t)
	// We use a stochastic test because we don't want
	// to mock rand and have detailed dependence on
	// the exact way it's used. We run the strategy many
	// times and note the delays that we found; if the
	// jitter is working, the delays should be roughly equally
	// distributed and it shouldn't take long before all the
	// buckets are hit.
	const numBuckets = 8
	tries := []struct {
		max     time.Duration
		buckets [numBuckets]int
	}{{
		max: 1e9,
	}, {
		max: 2e9,
	}, {
		max: 4e9,
	}, {
		max: 5e9,
	}}
	strategy := Strategy{
		Delay:    1e9,
		MaxDelay: 5e9,
	}
	count := 0
	now := time.Now()
	var i int
	for i = 0; i < 10000; i++ {
		var a Attempt
		a.Start(&strategy, nil, func() time.Time {
			return now
		})
		t, ok := a.NextTime()
		if !ok {
			c.Fatalf("no first try")
		}
		if !t.Equal(now) {
			c.Fatalf("first try was not immediate")
		}
		for try := 0; ; try++ {
			prevTime := now
			t, ok := a.NextTime()
			if !ok || try >= len(tries) {
				break
			}
			now = t
			d := now.Sub(prevTime)
			max := tries[try].max
			if d > max {
				c.Fatalf("try %d exceeded max %v; actual duration %v", try, tries[try].max, d)
			}
			slot := int(float64(d) / float64(max+1) * numBuckets)
			if slot >= numBuckets || slot < 0 {
				c.Fatalf("try %d slot %d out of range; d %v; max %v", try, slot, d, max)
			}
			buckets := &tries[try].buckets
			if buckets[slot] == 0 {
				count++
			}
			buckets[slot]++
		}
	}
	if count < len(tries)*numBuckets {
		c.Fatalf("distribution was not evenly spread; tries %#v", tries)
	}
}

func assertReceive(c *qt.C, ch <-chan struct{}, what string) {
	select {
	case <-ch:
	case <-time.After(time.Second):
		c.Fatalf("timed out waiting for %s", what)
	}
}

func noJitter(t testing.TB) {
	jitter = false
	t.Cleanup(func() {
		jitter = true
	})
}

func BenchmarkSimple(b *testing.B) {
	strategy := Strategy{
		Delay:    1,
		MaxCount: 10,
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var a Attempt
		for a.Start(&strategy, nil, nil); a.Next(); {
		}
	}
}
