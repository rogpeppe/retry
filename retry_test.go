package retry

import (
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func TestIterTiming(t *testing.T) {
	c := qt.New(t)
	testIter := Strategy{
		Delay:       0.1e9,
		MaxDuration: 0.25e9,
		Regular:     true,
	}
	want := []time.Duration{0, 0.1e9, 0.2e9, 0.2e9}
	got := make([]time.Duration, 0, len(want)) // avoid allocation when testing timing
	t0 := time.Now()
	i := testIter.Start(nil)
	for i.Next() {
		got = append(got, time.Now().Sub(t0))
	}
	got = append(got, time.Now().Sub(t0))
	c.Assert(i.WasStopped(), qt.Equals, false)
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

func TestIterWithStop(t *testing.T) {
	c := qt.New(t)
	stop := make(chan struct{})
	close(stop)
	done := make(chan struct{})
	go func() {
		strategy := Strategy{
			Delay:       5 * time.Second,
			MaxDuration: 30 * time.Second,
		}
		i := strategy.Start(stop)
		for i.Next() {
			c.Errorf("unexpected attempt")
		}
		c.Check(i.WasStopped(), qt.Equals, true)
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
	// delay holds the length of time that i call made at
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
		// so we get i zero delay, but subsequent events
		// resume pace.
		{2e9, 0},
		{2.1e9, 0.9e9},
		{3e9, 0},
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
			t0 := time.Now()
			now := t0
			test.strategy.Regular = true
			var i Iter
			i.Reset(&test.strategy, nil, func() time.Time {
				return now
			})
			for j, call := range test.calls {
				now = t0.Add(call.t)
				nextt, ok := i.NextTime()
				expectTerminate := test.terminates && j == len(test.calls)-1
				c.Assert(ok, qt.Equals, !expectTerminate)
				if ok {
					c.Logf("call %d at %v - got %v want %v", j, now.Sub(t0), nextt.Sub(now), call.sleep)
					if nextt.After(now) {
						now = nextt
					}
				} else {
					c.Logf("call %d - got nothing want %v", j, call.t)
					if !nextt.IsZero() {
						c.Fatalf("NextTime should return (time.Time{}, false)")
					}
				}
				// Allow for vagaries of floating point arithmetic.
				c.Check(now.Sub(t0), qt.CmpEquals(cmpopts.EquateApproxTime(20*time.Nanosecond)), call.t+call.sleep)
				if ok {
					c.Assert(i.Count(), qt.Equals, j+1)
				}
			}
		})
	}
}

func TestExponentialWithJitter(t *testing.T) {
	c := qt.New(t)
	// We use i stochastic test because we don't want
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
		var i Iter
		i.Reset(&strategy, nil, func() time.Time {
			return now
		})
		t, ok := i.NextTime()
		if !ok {
			c.Fatalf("no first try")
		}
		if !t.Equal(now) {
			c.Fatalf("first try was not immediate")
		}
		for try := 0; ; try++ {
			prevTime := now
			t, ok := i.NextTime()
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

var strategyStringTests = []struct {
	testName string
	s        Strategy
	want     string
}{{
	testName: "AllFields",
	s: Strategy{
		Delay:       time.Millisecond,
		MaxDelay:    30 * time.Second,
		Factor:      1.5,
		MaxCount:    20,
		MaxDuration: 2 * time.Minute,
	},
	want: "~1ms**1.5..30s; 20; 2m0s",
}, {
	testName: "AllFieldsRegular",
	s: Strategy{
		Regular:     true,
		Delay:       time.Millisecond,
		MaxDelay:    30 * time.Second,
		Factor:      1.5,
		MaxCount:    20,
		MaxDuration: 2 * time.Minute,
	},
	want: "1ms**1.5..30s; 20; 2m0s",
}, {
	testName: "NoMaxCount",
	s: Strategy{
		Regular:     true,
		Delay:       time.Millisecond,
		MaxDelay:    30 * time.Second,
		Factor:      1.5,
		MaxDuration: 2 * time.Minute,
	},
	want: "1ms**1.5..30s; ; 2m0s",
}, {
	testName: "NoMaxDuration",
	s: Strategy{
		Regular:  true,
		Delay:    time.Millisecond,
		MaxDelay: 30 * time.Second,
		Factor:   1.5,
		MaxCount: 20,
	},
	want: "1ms**1.5..30s; 20",
}, {
	testName: "NoMax",
	s: Strategy{
		Regular:  true,
		Delay:    time.Millisecond,
		MaxDelay: 30 * time.Second,
		Factor:   1.5,
	},
	want: "1ms**1.5..30s",
}, {
	testName: "DefaultFactor",
	s: Strategy{
		Delay:    time.Millisecond,
		MaxDelay: 30 * time.Second,
	},
	want: "~1ms..30s",
}, {
	testName: "Factor2",
	s: Strategy{
		Delay:    time.Millisecond,
		MaxDelay: 30 * time.Second,
	},
	want: "~1ms..30s",
}, {
	testName: "NonExponential",
	s: Strategy{
		Delay: time.Millisecond,
	},
	want: "~1ms",
}, {
	testName: "NonExponentialRegular",
	s: Strategy{
		Delay:   time.Millisecond,
		Regular: true,
	},
	want: "1ms",
}}

func TestStrategyString(t *testing.T) {
	c := qt.New(t)
	for _, test := range strategyStringTests {
		c.Run(test.testName, func(c *qt.C) {
			c.Assert(test.s.String(), qt.Equals, test.want)
		})
	}
}

func assertReceive(c *qt.C, ch <-chan struct{}, what string) {
	select {
	case <-ch:
	case <-time.After(time.Second):
		c.Fatalf("timed out waiting for %s", what)
	}
}

func BenchmarkReuseIter(b *testing.B) {
	strategy := Strategy{
		Delay:    1,
		MaxCount: 1,
	}
	b.ReportAllocs()
	var i Iter
	for j := 0; j < b.N; j++ {
		for i.Reset(&strategy, nil, nil); i.Next(); {
		}
	}
}

func BenchmarkStart(b *testing.B) {
	strategy := Strategy{
		Delay:    1,
		MaxCount: 1,
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		for i := strategy.Start(nil); i.Next(); {
		}
	}
}

func BenchmarkReuseIterWithStop(b *testing.B) {
	strategy := Strategy{
		Delay:    1,
		MaxCount: 1,
	}
	b.ReportAllocs()
	c := make(chan struct{})
	var i Iter
	for j := 0; j < b.N; j++ {
		for i.Reset(&strategy, c, nil); i.Next(); {
		}
	}
}

func BenchmarkStartWithStop(b *testing.B) {
	strategy := Strategy{
		Delay:    time.Microsecond,
		MaxCount: 5,
	}
	b.ReportAllocs()
	c := make(chan struct{})
	for j := 0; j < b.N; j++ {
		for i := strategy.Start(c); i.Next(); {
		}
	}
}

func BenchmarkReuseWithStop(b *testing.B) {
	strategy := Strategy{
		Delay:    time.Millisecond,
		MaxCount: 5,
	}
	b.ReportAllocs()
	c := make(chan struct{})
	var i Iter

	for j := 0; j < b.N; j++ {
		j := 0
		for i.Reset(&strategy, c, nil); i.Next(); {
			j++
		}
		if j != 5 {
			b.Fatal("unexpected count", j)
		}
	}
}
