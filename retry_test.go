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
	i := testIter.Start()
	for {
		got = append(got, time.Now().Sub(t0))
		if !i.Next(nil) {
			break
		}
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

func TestIterWithStopAlreadyClosed(t *testing.T) {
	c := qt.New(t)
	stop := make(chan struct{})
	close(stop)
	strategy := Strategy{
		Delay:       5 * time.Second,
		MaxDuration: 30 * time.Second,
		Regular:     true,
	}
	i := strategy.Start()
	if i.Next(stop) {
		c.Fatal("unexpected attempt")
	}
	c.Check(i.WasStopped(), qt.Equals, true)
}

func TestIterWithStopNotStopped(t *testing.T) {
	strategy := Strategy{
		Delay:       time.Millisecond,
		MaxDuration: 100 * time.Millisecond,
		Regular:     true,
	}
	t0 := time.Now()
	stop := make(chan struct{})
	for i := strategy.Start(); i.Next(stop); {
	}
	duration := time.Since(t0)
	if duration < strategy.MaxDuration-strategy.Delay {
		t.Fatalf("loop terminated too early; got %v want %v", duration, strategy.MaxDuration)
	}
	if duration > strategy.MaxDuration+500*time.Millisecond {
		t.Fatalf("loop terminated too late; got %v want %v", duration, strategy.MaxDuration)
	}
}

func TestCount(t *testing.T) {
	c := qt.New(t)
	strategy := Strategy{
		Delay:    time.Nanosecond,
		MaxCount: 2,
	}
	i := strategy.Start()
	c.Assert(i.Count(), qt.Equals, 1)
	c.Assert(i.Next(nil), qt.IsTrue)
	c.Assert(i.Count(), qt.Equals, 2)
	_, ok := i.NextTime()
	c.Assert(ok, qt.IsFalse)
	c.Assert(i.Count(), qt.Equals, 2)
}

func TestIterWithStopWhileSleeping(t *testing.T) {
	c := qt.New(t)
	stop := make(chan struct{})
	go func() {
		time.Sleep(200 * time.Millisecond)
		close(stop)
	}()
	strategy := Strategy{
		Delay:       5 * time.Second,
		MaxDuration: 30 * time.Second,
		Regular:     true,
	}
	t0 := time.Now()
	i := strategy.Start()
	count := 0
	for {
		count++
		if !i.Next(stop) {
			break
		}
	}
	c.Check(i.WasStopped(), qt.Equals, true)
	c.Check(i.Count(), qt.Equals, 1)
	c.Check(count, qt.Equals, 1)
	if d := time.Since(t0); d > 500*time.Millisecond {
		c.Fatalf("loop didn't stop when stop channel was closed; got %v want %v", d, 200*time.Millisecond)
	}
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
			i.Reset(&test.strategy, func() time.Time {
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
					c.Assert(i.Count(), qt.Equals, j+2)
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
		i.Reset(&strategy, func() time.Time {
			return now
		})
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
	want: "delay=1ms maxdelay=30s factor=1.5 maxcount=20 maxduration=2m0s",
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
	want: "delay=1ms maxdelay=30s factor=1.5 regular=true maxcount=20 maxduration=2m0s",
}, {
	testName: "NoMaxCount",
	s: Strategy{
		Regular:     true,
		Delay:       time.Millisecond,
		MaxDelay:    30 * time.Second,
		Factor:      1.5,
		MaxDuration: 2 * time.Minute,
	},
	want: "delay=1ms maxdelay=30s factor=1.5 regular=true maxduration=2m0s",
}, {
	testName: "NoMaxDuration",
	s: Strategy{
		Regular:  true,
		Delay:    time.Millisecond,
		MaxDelay: 30 * time.Second,
		Factor:   1.5,
		MaxCount: 20,
	},
	want: "delay=1ms maxdelay=30s factor=1.5 regular=true maxcount=20",
}, {
	testName: "NoMax",
	s: Strategy{
		Regular:  true,
		Delay:    time.Millisecond,
		MaxDelay: 30 * time.Second,
		Factor:   1.5,
	},
	want: "delay=1ms maxdelay=30s factor=1.5 regular=true",
}, {
	testName: "DefaultFactor",
	s: Strategy{
		Delay:    time.Millisecond,
		MaxDelay: 30 * time.Second,
	},
	want: "delay=1ms maxdelay=30s",
}, {
	testName: "Factor2",
	s: Strategy{
		Delay:    time.Millisecond,
		MaxDelay: 30 * time.Second,
		Factor:   2,
	},
	want: "delay=1ms maxdelay=30s factor=2",
}, {
	testName: "NonExponential",
	s: Strategy{
		Delay: time.Millisecond,
	},
	want: "delay=1ms",
}, {
	testName: "NonExponentialRegular",
	s: Strategy{
		Delay:   time.Millisecond,
		Regular: true,
	},
	want: "delay=1ms regular=true",
}}

func TestStrategyString(t *testing.T) {
	c := qt.New(t)
	for _, test := range strategyStringTests {
		c.Run(test.testName, func(c *qt.C) {
			s := test.s.String()
			c.Assert(s, qt.Equals, test.want)
			// Check that we can parse the result.
			st, err := ParseStrategy(s)
			c.Assert(err, qt.IsNil, qt.Commentf("string: %q", s))
			c.Assert(st, qt.DeepEquals, &test.s)
		})
	}
}

var parseStrategyTests = []struct {
	testName    string
	str         string
	expect      Strategy
	expectError string
}{{
	testName: "SimpleRegular",
	str:      "delay=1ms regular=true",
	expect: Strategy{
		Regular: true,
		Delay:   time.Millisecond,
	},
}, {
	testName: "SimpleWithJitter",
	str:      "delay=1ms",
	expect: Strategy{
		Delay: time.Millisecond,
	},
}, {
	testName:    "Empty",
	str:         "",
	expectError: `no delay field found`,
}, {
	testName:    "OnlySpace",
	str:         "   ",
	expectError: `no delay field found`,
}, {
	testName:    "NoEquals",
	str:         "delay",
	expectError: `no = found after field`,
}, {
	testName: "DelayWithDecimalPoint",
	str:      "delay=.5s",
	expect: Strategy{
		Delay: 500 * time.Millisecond,
	},
}, {
	testName:    "SpaceAroundEqual",
	str:         "delay = 1s",
	expectError: `no = found after field`,
}, {
	testName: "ExplicitFalseRegular",
	str:      "delay=1s regular=false",
	expect: Strategy{
		Delay: time.Second,
	},
}, {
	testName:    "BadRegular",
	str:         "delay=1s regular=0",
	expectError: `cannot parse field  'regular': invalid boolean value '0'`,
}, {
	testName:    "RepeatedField",
	str:         "delay=1s delay=2s",
	expectError: `repeated field 'delay'`,
}, {
	testName:    "BadDelay",
	str:         "delay=x",
	expectError: `cannot parse field  'delay': time: invalid duration "x"`,
}, {
	testName:    "MissingFactor",
	str:         "factor=",
	expectError: `no value found for field 'factor'`,
}, {
	testName:    "BadFactor",
	str:         "factor=x",
	expectError: `cannot parse field  'factor': strconv.ParseFloat: parsing "x": invalid syntax`,
}, {
	testName: "AllFieldsWithWhiteSpace",
	str:      "   delay=1ms    maxdelay=30s   regular=true   factor=1.5   maxcount=20  maxduration=2m   ",
	expect: Strategy{
		Delay:       time.Millisecond,
		MaxDelay:    30 * time.Second,
		Regular:     true,
		Factor:      1.5,
		MaxCount:    20,
		MaxDuration: 2 * time.Minute,
	},
}, {
	testName:    "BadMaxDelay",
	str:         "maxdelay=x",
	expectError: `cannot parse field  'maxdelay': time: invalid duration "x"`,
}, {
	testName:    "BadMaxCount",
	str:         "maxcount=x",
	expectError: `cannot parse field  'maxcount': strconv.Atoi: parsing "x": invalid syntax`,
}, {
	testName:    "UnknownField",
	str:         "foo=bar",
	expectError: `unknown field 'foo'`,
}, {
	testName:    "BadMaxDuration",
	str:         "maxduration=x",
	expectError: `cannot parse field  'maxduration': time: invalid duration "x"`,
}}

func TestParseStrategy(t *testing.T) {
	c := qt.New(t)
	for _, test := range parseStrategyTests {
		c.Run(test.testName, func(c *qt.C) {
			st, err := ParseStrategy(test.str)
			if test.expectError != "" {
				c.Assert(err, qt.ErrorMatches, test.expectError)
				return
			}
			c.Assert(err, qt.IsNil)
			c.Assert(st, qt.DeepEquals, &test.expect)
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
		for i.Reset(&strategy, nil); i.Next(nil); {
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
		for i := strategy.Start(); i.Next(nil); {
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
		for i.Reset(&strategy, nil); i.Next(c); {
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
		for i := strategy.Start(); i.Next(c); {
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
		for i.Reset(&strategy, nil); i.Next(c); {
			j++
		}
		if j != 5 {
			b.Fatal("unexpected count", j)
		}
	}
}
