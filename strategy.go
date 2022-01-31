package retry

import (
	"errors"
	"strconv"
	"time"
)

// Note: we're deliberately avoiding the use of the strings and fmt packages
// here to keep this package as low level as possible.

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
	// maximum of MaxDelay if that's non-zero.
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

func (s *Strategy) isExponential() bool {
	return s.MaxDelay > s.Delay || s.Factor > 0
}

// String returns the strategy in the format understood by ParseStrategy.
func (s *Strategy) String() string {
	buf := make([]byte, 0, 20)

	buf = append(buf, "delay="...)
	buf = append(buf, s.Delay.String()...)
	if s.MaxDelay > s.Delay {
		buf = append(buf, " maxdelay="...)
		buf = append(buf, s.MaxDelay.String()...)
		factor := s.Factor
		if factor != 0 && factor <= 1 {
			factor = 2
		}
		if factor > 0 {
			buf = append(buf, " factor="...)
			buf = strconv.AppendFloat(buf, factor, 'g', -1, 64)
		}
	}
	if s.Regular {
		buf = append(buf, " regular=true"...)
	}
	if s.MaxCount > 0 {
		buf = append(buf, " maxcount="...)
		buf = strconv.AppendInt(buf, int64(s.MaxCount), 10)
	}
	if s.MaxDuration > 0 {
		buf = append(buf, " maxduration="...)
		buf = append(buf, s.MaxDuration.String()...)
	}
	return string(buf)
}

var fieldParsers = []struct {
	name  string
	parse func(*Strategy, string) error
}{{
	// Note: this must be first in the list.
	name:  "delay",
	parse: parseDelay,
}, {
	name:  "maxdelay",
	parse: parseMaxDelay,
}, {
	name:  "regular",
	parse: parseRegular,
}, {
	name:  "factor",
	parse: parseFactor,
}, {
	name:  "maxcount",
	parse: parseMaxCount,
}, {
	name:  "maxduration",
	parse: parseMaxDuration,
}}

// ParseStrategy parses a string representation of a strategy. It takes
// the form of space-separated attribute=value pairs, where the available
// attributes are the lower-cased field names from the Strategy type.
// Values of time.Duration type are parsed with time.ParseDuration;
// floats are parsed with strconv.ParseFloat and boolean values
// must be either "true" or "false".
//
// Fields may be in any order but must not be repeated.
//
// For example:
//
//	delay=1ms maxdelay=1s regular=true factor=1.5 maxcount=10 maxduration=5m
//
// The delay field is mandatory.
func ParseStrategy(s string) (*Strategy, error) {
	i := 0
	foundMask := 0
	var st Strategy
	for {
		i = skipSpace(s, i)
		if i >= len(s) {
			break
		}
		j := find(s, i, '=')
		if j == len(s) || s[j] != '=' {
			return nil, errors.New("no = found after field")
		}
		field := s[i:j]
		i = j
		found := -1
		for k := range fieldParsers {
			if fieldParsers[k].name == field {
				found = k
				break
			}
		}
		if found == -1 {
			return nil, errors.New("unknown field '" + field + "'")
		}
		if (foundMask & (1 << found)) != 0 {
			return nil, errors.New("repeated field '" + field + "'")
		}
		foundMask |= 1 << found

		i++ // Skip "="
		j = find(s, i, ' ')
		if j == i {
			return nil, errors.New("no value found for field '" + field + "'")
		}
		value := s[i:j]
		if err := fieldParsers[found].parse(&st, value); err != nil {
			return nil, errors.New("cannot parse field  '" + field + "': " + err.Error())
		}
		i = j
	}
	if (foundMask & 1) == 0 {
		return nil, errors.New("no delay field found")
	}
	return &st, nil
}

func parseDelay(st *Strategy, s string) (err error) {
	st.Delay, err = time.ParseDuration(s)
	return
}

func parseMaxDelay(st *Strategy, s string) (err error) {
	st.MaxDelay, err = time.ParseDuration(s)
	return
}

func parseRegular(st *Strategy, s string) (err error) {
	switch s {
	case "true":
		st.Regular = true
	case "false":
		st.Regular = false
	default:
		return errors.New("invalid boolean value '" + s + "'")
	}
	return nil
}

func parseFactor(st *Strategy, s string) (err error) {
	st.Factor, err = strconv.ParseFloat(s, 64)
	return
}

func parseMaxCount(st *Strategy, s string) (err error) {
	st.MaxCount, err = strconv.Atoi(s)
	return
}

func parseMaxDuration(st *Strategy, s string) (err error) {
	st.MaxDuration, err = time.ParseDuration(s)
	return
}

func find(s string, i int, want byte) int {
	for ; i < len(s); i++ {
		if c := s[i]; c == want || c == ' ' {
			return i
		}
	}
	return i
}

func skipSpace(s string, i int) int {
	for i < len(s) && s[i] == ' ' {
		i++
	}
	return i
}
