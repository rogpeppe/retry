# retry

Package retry implements an efficient loop-based retry mechanism
that allows the retry policy to be specified independently of the control structure.
It supports exponential (with jitter) and linear retry policies.

Although syntactically lightweight, it's also flexible - for example,
it can be used to run a backoff loop while waiting for other concurrent
events, or with mocked-out time.

## Types

### type [Iter](https://github.com/rogpeppe/retry/blob/master/retry.go#L18)

`type Iter struct { ... }`

Iter represents a particular retry iteration loop using some strategy.

#### func (*Iter) [Count](https://github.com/rogpeppe/retry/blob/master/retry.go#L181)

`func (i *Iter) Count() int`

Count returns the number of iterations so far. Specifically,
this returns the number of times that Next or NextTime have returned true.

#### func (*Iter) [Next](https://github.com/rogpeppe/retry/blob/master/retry.go#L93)

`func (i *Iter) Next(stop <-chan struct{ ... }) bool`

Next sleeps until the next iteration is to be made and
reports whether there are any more iterations remaining.

If a value is received on the stop channel, it immediately
stops waiting for the next iteration and returns false.
i.WasStopped can be called to determine if that happened.

#### func (*Iter) [NextTime](https://github.com/rogpeppe/retry/blob/master/retry.go#L110)

`func (i *Iter) NextTime() (time.Time, bool)`

NextTime is similar to Next except that it instead returns
immediately with the time that the next iteration should begin.
The caller is responsible for actually waiting until that time.

#### func (*Iter) [Reset](https://github.com/rogpeppe/retry/blob/master/retry.go#L53)

`func (i *Iter) Reset(strategy *Strategy, now func() time.Time)`

Reset is like Strategy.Start but initializes an existing Iter
value which can save the allocation of the underlying
time.Timer used when Next is called with a non-nil stop channel.

It also accepts a function that is used to get the current time.
If that's nil, time.Now will be used.

It's OK to call this on the zero Iter value.

#### func (*Iter) [StartTime](https://github.com/rogpeppe/retry/blob/master/retry.go#L137)

`func (i *Iter) StartTime() time.Time`

StartTime returns the time that the
iterator was created or last reset.

#### func (*Iter) [TryTime](https://github.com/rogpeppe/retry/blob/master/retry.go#L128)

`func (i *Iter) TryTime() (time.Time, bool)`

TryTime returns the time that the current try iteration should be
made at, if there should be one. If iteration has finished, it
returns (time.Time{}, false).

The returned time can be in the past (after Start or Reset or Next
have been called) or in the future (after NextTime has been called,
TryTime returns the same values that NextTime returned).

Calling TryTime repeatedly will return the same values until
Next or NextTime or Reset have been called.

#### func (*Iter) [WasStopped](https://github.com/rogpeppe/retry/blob/master/retry.go#L83)

`func (i *Iter) WasStopped() bool`

WasStopped reports whether the most recent call to Next
was stopped because a value was received on its stop channel.

### type [Strategy](https://github.com/rogpeppe/retry/blob/master/strategy.go#L25)

`type Strategy struct { ... }`

Strategy represents a retry strategy. This specifies how a set of retries should
be made and can be reused for any number of loops (it's treated as immutable
by this package).

If an iteration takes longer than the delay for that iteration, the next
iteration will be moved accordingly. For example, if the strategy
has a delay of 1ms and the first two tries take 1s and 0.5ms respectively,
then the second try will start immediately after the first (at 1s), but
the third will start at 1.1s.

All strategies will loop for at least one iteration. The only time a loop
might terminate immediately is when a value is received on
the stop channel.

#### func [ParseStrategy](https://github.com/rogpeppe/retry/blob/master/strategy.go#L142)

`func ParseStrategy(s string) (*Strategy, error)`

ParseStrategy parses a string representation of a strategy. It takes
the form of space-separated attribute=value pairs, where the available
attributes are the lower-cased field names from the Strategy type.
Values of time.Duration type are parsed with time.ParseDuration;
floats are parsed with strconv.ParseFloat and boolean values
must be either "true" or "false".

Fields may be in any order but must not be repeated.

For example:

```go
delay=1ms maxdelay=1s regular=true factor=1.5 maxcount=10 maxduration=5m
```

The delay field is mandatory.

#### func (*Strategy) [Start](https://github.com/rogpeppe/retry/blob/master/retry.go#L39)

`func (s *Strategy) Start() *Iter`

Start starts a retry loop using s as a retry strategy and
returns an Iter that can be used to wait for each retry
in turn. Note: the first try should be made immediately
after calling Start without calling Next.

#### func (*Strategy) [String](https://github.com/rogpeppe/retry/blob/master/strategy.go#L73)

`func (s *Strategy) String() string`

String returns the strategy in the format understood by ParseStrategy.

---
Readme created from Go doc with [goreadme](https://github.com/posener/goreadme)
