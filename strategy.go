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

func (s *Strategy) isExponential() bool {
	return s.MaxDelay > s.Delay || s.Factor > 0
}

// ParseStrategy parses a string representation of a strategy, in the form.
//
// 	~1ms**1.5..30s; 20; 2m
//
// The grammar is:
//
// 	[ "~" ] minDelay [ [ "**" factor ] ".." maxDelay ] [ ";" [ maxCount ] [ ";" [ maxDuration ] ]
//
// Space characters are allowed before or after any token.
// The "~" prefix signifies whether there is jitter or not (if present, Regular will be false).
func ParseStrategy(s string) (*Strategy, error) {

	var st Strategy
	i := skipSpace(s, 0)
	i, ok := skipPrefix(s, i, "~")
	if ok {
		i = skipSpace(s, i)
	} else {
		st.Regular = true
	}
	j := find(s, i, "*;. ")
	if j == i {
		return nil, errors.New("no delay found")
	}
	d, err := time.ParseDuration(s[i:j])
	if err != nil {
		return nil, errors.New("invalid delay: " + err.Error())
	}
	st.Delay = d
	i = skipSpace(s, j)
	if i == len(s) {
		return &st, nil
	}
	if s[i] == '*' || s[i] == '.' {
		if s[i] == '*' {
			j, ok := skipPrefix(s, i, "**")
			if !ok {
				return nil, errors.New("invalid exponential factor prefix")
			}
			i = skipSpace(s, j)
			j = find(s, i, ".; ")
			if j == i {
				return nil, errors.New("missing exponential factor")
			}
			factor, err := strconv.ParseFloat(s[i:j], 64)
			if err != nil {
				return nil, errors.New("invalid exponential factor: " + err.Error())
			}
			st.Factor = factor
			i = j
			i = skipSpace(s, i)
			if i == len(s) || s[i] != '.' {
				return nil, errors.New(`exponential factor must be followed by ".."`)
			}
		}
		j, ok := skipPrefix(s, i, "..")
		if !ok {
			return nil, errors.New("invalid .. prefix")
		}
		i = skipSpace(s, j)
		j = find(s, i, "; ")
		if j == i {
			return nil, errors.New(`missing max delay after ".."`)
		}
		maxDelay, err := time.ParseDuration(s[i:j])
		if err != nil {
			return nil, errors.New("invalid max delay: " + err.Error())
		}
		st.MaxDelay = maxDelay
		i = skipSpace(s, j)
		if i == len(s) {
			return &st, nil
		}
	}
	if s[i] != ';' {
		panic("must be semicolon")
	}
	i++ // skip semicolon
	i = skipSpace(s, i)
	j = find(s, i, " ;")
	if j > i {
		maxCount, err := strconv.Atoi(s[i:j])
		if err != nil {
			return nil, errors.New("invalid max count: " + err.Error())
		}
		st.MaxCount = int(maxCount)
		i = j
	}
	i = skipSpace(s, i)
	if i == len(s) {
		return &st, nil
	}
	if s[i] != ';' {
		panic("must be second semicolon")
	}
	i++
	i = skipSpace(s, i)
	j = find(s, i, " ")
	if j == i {
		return &st, nil
	}
	maxDuration, err := time.ParseDuration(s[i:j])
	if err != nil {
		return nil, errors.New("invalid max duration: " + err.Error())
	}
	st.MaxDuration = maxDuration
	i = skipSpace(s, j)
	if i != len(s) {
		return nil, errors.New("unexpected text after strategy")
	}
	return &st, nil
}

func find(s string, i int, oneof string) int {
	for ; i < len(s); i++ {
		c := s[i]
		for j := 0; j < len(oneof); j++ {
			if c == oneof[j] {
				if c != '.' {
					return i
				}
				// Special case: if we're looking for a dot, we
				// are actually looking for "..".
				if i+1 < len(s) && s[i+1] == '.' {
					return i
				}
			}
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

func skipPrefix(s string, i int, prefix string) (int, bool) {
	t := s[i:]
	if len(t) < len(prefix) || t[:len(prefix)] != prefix {
		return i, false
	}
	return i + len(prefix), true
}

// String returns the strategy in the format understood by ParseStrategy.
func (s *Strategy) String() string {
	buf := make([]byte, 0, 20)

	if !s.Regular {
		buf = append(buf, '~')
	}
	buf = append(buf, s.Delay.String()...)
	if s.MaxDelay > s.Delay {
		factor := s.Factor
		if factor <= 1 {
			factor = 2
		}
		if factor != 2 {
			buf = append(buf, "**"...)
			buf = strconv.AppendFloat(buf, factor, 'g', -1, 64)
		}
		buf = append(buf, ".."...)
		buf = append(buf, s.MaxDelay.String()...)
	}
	if s.MaxCount > 0 || s.MaxDuration > 0 {
		buf = append(buf, "; "...)
		if s.MaxCount > 0 {
			buf = strconv.AppendInt(buf, int64(s.MaxCount), 10)
		}
		if s.MaxDuration > 0 {
			buf = append(buf, "; "...)
			buf = append(buf, s.MaxDuration.String()...)
		}
	}
	return string(buf)
}
