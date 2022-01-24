
package retry_test

import (
	"container/heap"
	"context"
	"fmt"
	"log"
	"time"

	"github.com/rogpeppe/retry"
)

// This example demonstrates running multiple retry iterators
// simultaneously, using container/heap to manage them.
func Example_multiple() {
	log.SetFlags(log.Lmicroseconds)

	ctx := context.Background()
	var h retryHeap
	for i := 0; i < 5; i++ {
		h.Push(retryItem{
			name: fmt.Sprintf("item%d", i),
			iter: retryStrategy.Start(),
		})
	}
	for {
		var c <-chan time.Time
		for h.Len() > 0 {
			t, ok := h[0].iter.TryTime()
			if !ok {
				log.Printf("%s finished\n", h[0].name)
				heap.Pop(&h)
				continue
			}
			if wait := time.Until(t); wait > 0 {
				c = time.After(wait)
				break
			}
			log.Printf("%s try %d\n", h[0].name, h[0].iter.Count())
			h[0].iter.NextTime()
			heap.Fix(&h, 0)
		}
		if c == nil {
			log.Printf("all done\n")
			return
		}
		select {
		case <-c:
		case <-ctx.Done():
			log.Printf("context is done: %v", ctx.Err())
			return
		}
	}
}

var multipleStrategy = retry.Strategy{
	Delay:       100 * time.Millisecond,
	MaxDelay:    5 * time.Second,
	MaxDuration: 10 * time.Second,
	Factor:      2,
	Regular:     true,
}

type retryItem struct {
	iter *retry.Iter
	name string
}

type retryHeap []retryItem

func (h retryHeap) Less(i, j int) bool {
	t1, _ := h[i].iter.TryTime()
	t2, _ := h[j].iter.TryTime()
	return t1.Before(t2)
}

func (h retryHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
}

func (h retryHeap) Len() int {
	return len(h)
}

func (h *retryHeap) Push(x interface{}) {
	*h = append(*h, x.(retryItem))
}

func (h *retryHeap) Pop() interface{} {
	n := len(*h)
	i := (*h)[n-1]
	(*h) = (*h)[:n-1]
	return i
}
