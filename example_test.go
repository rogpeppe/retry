package retry_test

import (
	"fmt"
	"log"
	"math/rand"
	"time"

	"github.com/rogpeppe/retry"
)

type Foo struct{}

func Example() {
	log.SetFlags(log.Lmicroseconds)
	_, err := getFooWithRetry()
	if err != nil {
		log.Printf("getFooWithRetry: %v", err)
	} else {
		log.Printf("getFooWithRetry: ok")
	}
}

var retryStrategy = retry.Strategy{
	Delay:       100 * time.Millisecond,
	MaxDelay:    5 * time.Second,
	MaxDuration: 10 * time.Second,
	Factor:      2,
}

// getFooWithRetry demonstrates a retry loop.
func getFooWithRetry() (*Foo, error) {
	for i := retryStrategy.Start(); ; {
		log.Printf("getting foo")
		foo, err := getFoo()
		if err == nil {
			return foo, nil
		}
		if !i.Next(nil) {
			return nil, fmt.Errorf("error getting foo after %d tries: %v", i.Count(), err)
		}
	}
}

func getFoo() (*Foo, error) {
	if rand.Intn(5000) == 0 {
		return &Foo{}, nil
	}
	return nil, fmt.Errorf("some error")
}
