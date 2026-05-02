package weft

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestTraverse_Sequential(t *testing.T) {
	f := ArrowFunc(func(_ context.Context, n int) (int, error) { return n * 2, nil })
	got, err := Traverse(f)(context.Background(), []int{1, 2, 3, 4, 5})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []int{2, 4, 6, 8, 10}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("at %d: got %d, want %d", i, got[i], want[i])
		}
	}
}

func TestTraverse_Concurrent(t *testing.T) {
	delay := 50 * time.Millisecond
	f := ArrowFunc(func(_ context.Context, n int) (int, error) {
		time.Sleep(delay)
		return n * 2, nil
	})

	start := time.Now()
	_, err := Traverse(f, WithConcurrency(5))(context.Background(), []int{1, 2, 3, 4, 5})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 5 items × 50ms sequential = 250ms; concurrent should be ~50ms.
	if elapsed > delay*2 {
		t.Errorf("Traverse did not run concurrently: elapsed=%v", elapsed)
	}
}

func TestTraverse_ConcurrencyLimit(t *testing.T) {
	maxConcurrent := atomic.Int32{}
	current := atomic.Int32{}
	f := ArrowFunc(func(_ context.Context, n int) (int, error) {
		c := current.Add(1)
		// Track peak.
		for {
			peak := maxConcurrent.Load()
			if c <= peak || maxConcurrent.CompareAndSwap(peak, c) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		current.Add(-1)
		return n * 2, nil
	})

	items := make([]int, 20)
	for i := range items {
		items[i] = i
	}

	_, err := Traverse(f, WithConcurrency(3))(context.Background(), items)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if peak := maxConcurrent.Load(); peak > 3 {
		t.Errorf("concurrency limit violated: peak=%d, limit=3", peak)
	}
}

func TestTraverse_FailFast(t *testing.T) {
	f := ArrowFunc(func(_ context.Context, n int) (int, error) {
		if n == 3 {
			return 0, errors.New("rejecting 3")
		}
		return n * 2, nil
	})

	_, err := Traverse(f, WithConcurrency(1), OnError(FailFast))(
		context.Background(), []int{1, 2, 3, 4, 5})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestTraverse_PartialResults(t *testing.T) {
	f := ArrowFunc(func(_ context.Context, n int) (int, error) {
		if n%2 == 0 {
			return 0, errors.New("rejecting even")
		}
		return n * 10, nil
	})

	got, err := Traverse(f, WithConcurrency(2), OnError(PartialResults))(
		context.Background(), []int{1, 2, 3, 4, 5})

	var partial *PartialError
	if !errors.As(err, &partial) {
		t.Fatalf("expected PartialError, got %v", err)
	}
	if partial.Total != 5 {
		t.Errorf("Total=%d, want 5", partial.Total)
	}
	if len(partial.Failures) != 2 { // indices 1, 3 (values 2, 4)
		t.Errorf("got %d failures, want 2", len(partial.Failures))
	}
	// Successful results should be at their original indices
	if got[0] != 10 || got[2] != 30 || got[4] != 50 {
		t.Errorf("results not at original indices: %v", got)
	}
}

func TestTraverse_CollectErrors(t *testing.T) {
	f := ArrowFunc(func(_ context.Context, n int) (int, error) {
		if n%2 == 0 {
			return 0, errors.New("even")
		}
		return n, nil
	})

	_, err := Traverse(f, WithConcurrency(2), OnError(CollectErrors))(
		context.Background(), []int{1, 2, 3, 4})
	if err == nil {
		t.Fatal("expected joined error")
	}
}

func TestTraverse_SkipFailures(t *testing.T) {
	f := ArrowFunc(func(_ context.Context, n int) (int, error) {
		if n%2 == 0 {
			return 0, errors.New("even")
		}
		return n * 100, nil
	})

	got, err := Traverse(f, WithConcurrency(2), OnError(SkipFailures))(
		context.Background(), []int{1, 2, 3, 4, 5})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []int{100, 300, 500}
	if len(got) != len(want) {
		t.Fatalf("got %d items, want %d: %v", len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("at %d: got %d, want %d", i, got[i], want[i])
		}
	}
}
