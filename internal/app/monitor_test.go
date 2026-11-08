package app

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A fake host: its counter is the number of samples taken; a rate is the
// pair of counters it was made of.
func fakeShared(fail *atomic.Bool) (*shared[int, [2]int], *atomic.Int32, *time.Time) {
	var n atomic.Int32
	clock := time.Unix(1_800_000_000, 0)
	s := newShared(func(ctx context.Context, id string) (int, error) {
		if fail != nil && fail.Load() {
			return 0, errors.New("host gone")
		}
		return int(n.Add(1)), nil
	}, func(prev, cur int) [2]int { return [2]int{prev, cur} })
	s.now = func() time.Time { return clock }
	return s, &n, &clock
}

func TestSharedSamples(t *testing.T) {
	ctx := context.Background()
	s, n, clock := fakeShared(nil)
	get := func() [2]int {
		t.Helper()
		r, err := s.get(ctx, "a")
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := get(); r != [2]int{0, 1} {
		t.Errorf("first: %v", r)
	}
	// A second page within the interval gets the same, without a sample.
	*clock = clock.Add(time.Second)
	if r := get(); r != [2]int{0, 1} || n.Load() != 1 {
		t.Errorf("within the interval: %v, %d samples", r, n.Load())
	}
	*clock = clock.Add(time.Second)
	if r := get(); r != [2]int{1, 2} {
		t.Errorf("next: %v", r)
	}
	// After a long pause the old sample is no start.
	*clock = clock.Add(time.Minute)
	if r := get(); r != [2]int{0, 3} {
		t.Errorf("after a pause: %v", r)
	}
	// Each connection has its own.
	if r, _ := s.get(ctx, "b"); r != [2]int{0, 4} {
		t.Errorf("another connection: %v", r)
	}
	s.forget("a")
	if r := get(); r != [2]int{0, 5} {
		t.Errorf("forgotten: %v", r)
	}
}

func TestSharedSamplesConcurrent(t *testing.T) {
	s, n, _ := fakeShared(nil)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.get(context.Background(), "a")
		}()
	}
	wg.Wait()
	if n.Load() != 1 {
		t.Errorf("20 pages at once: %d samples, want 1", n.Load())
	}
}

func TestSharedSamplesError(t *testing.T) {
	var fail atomic.Bool
	s, _, clock := fakeShared(&fail)
	s.get(context.Background(), "a")
	*clock = clock.Add(2 * time.Second)
	fail.Store(true)
	if _, err := s.get(context.Background(), "a"); err == nil {
		t.Error("no error from a failed sample")
	}
	fail.Store(false)
	if r, _ := s.get(context.Background(), "a"); r != [2]int{1, 2} {
		t.Errorf("after an error: %v", r)
	}
}
