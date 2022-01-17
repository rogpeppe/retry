package retry_test

import (
	"fmt"
	"time"

	"github.com/rogpeppe/retry"
)

type Foo struct{}

func ExampleStrategy() {
	getFoo()
}

var retryStrategy = retry.Strategy{
	Delay:       time.Millisecond,
	MaxDelay:    time.Second,
	MaxDuration: time.Minute,
}

// getFooWithRetry demonstrates a retry loop.
func getFooWithRetry() (*Foo, error) {
	for i := retryStrategy.Start(nil); i.Next(); {
		if foo, err := getFoo(); err == nil {
			return foo, nil
		}
	}
	return nil, fmt.Errorf("too many retries")
}

func getFoo() (*Foo, error) {
	return &Foo{}, nil
}
