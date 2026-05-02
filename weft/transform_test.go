package weft

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestApply_OutermostFirst(t *testing.T) {
	// Verify that Apply applies transforms in left-to-right order
	// such that the leftmost transform wraps everything else.
	order := []string{}
	addLog := func(name string) Transform[int, int] {
		return func(inner Arrow[int, int]) Arrow[int, int] {
			return func(ctx context.Context, n int) (int, error) {
				order = append(order, "before:"+name)
				result, err := inner(ctx, n)
				order = append(order, "after:"+name)
				return result, err
			}
		}
	}

	base := ArrowFunc(func(_ context.Context, n int) (int, error) { return n, nil })
	wrapped := Apply(base, addLog("outer"), addLog("inner"))
	_, _ = wrapped(context.Background(), 1)

	expected := []string{"before:outer", "before:inner", "after:inner", "after:outer"}
	if len(order) != len(expected) {
		t.Fatalf("got order %v, want %v", order, expected)
	}
	for i := range order {
		if order[i] != expected[i] {
			t.Errorf("at %d: got %s, want %s", i, order[i], expected[i])
		}
	}
}

func TestWithRetry_RetriesUntilSuccess(t *testing.T) {
	attempts := atomic.Int32{}
	flaky := ArrowFunc(func(_ context.Context, n int) (int, error) {
		a := attempts.Add(1)
		if a < 3 {
			return 0, errors.New("transient")
		}
		return n * 2, nil
	})

	wrapped := WithRetry[int, int](5, LinearBackoff(time.Millisecond))(flaky)
	got, err := wrapped(context.Background(), 21)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 42 {
		t.Errorf("got %d, want 42", got)
	}
	if attempts.Load() != 3 {
		t.Errorf("attempts=%d, want 3", attempts.Load())
	}
}

func TestWithRetry_GivesUpAfterMaxAttempts(t *testing.T) {
	attempts := atomic.Int32{}
	always := ArrowFunc(func(_ context.Context, n int) (int, error) {
		attempts.Add(1)
		return 0, errors.New("always fails")
	})

	wrapped := WithRetry[int, int](3, LinearBackoff(time.Millisecond))(always)
	_, err := wrapped(context.Background(), 1)
	if err == nil {
		t.Fatal("expected error after max attempts")
	}
	if attempts.Load() != 3 {
		t.Errorf("attempts=%d, want 3", attempts.Load())
	}
}

func TestWithRetry_DoesNotRetryPermanentErrors(t *testing.T) {
	attempts := atomic.Int32{}
	permanentFail := ArrowFunc(func(_ context.Context, n int) (int, error) {
		attempts.Add(1)
		return 0, &ArrowError{
			Class: ClassPermanent,
			Op:    "test",
			Cause: errors.New("permanent failure"),
		}
	})

	wrapped := WithRetry[int, int](5, LinearBackoff(time.Millisecond))(permanentFail)
	_, err := wrapped(context.Background(), 1)
	if err == nil {
		t.Fatal("expected error")
	}
	if attempts.Load() != 1 {
		t.Errorf("attempts=%d, want 1 (no retries on permanent)", attempts.Load())
	}
}

func TestWithTimeout_FastPath(t *testing.T) {
	fast := ArrowFunc(func(_ context.Context, n int) (int, error) {
		return n + 1, nil
	})
	wrapped := WithTimeout[int, int](100 * time.Millisecond)(fast)
	got, err := wrapped(context.Background(), 41)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 42 {
		t.Errorf("got %d, want 42", got)
	}
}

func TestWithTimeout_SlowPathTimesOut(t *testing.T) {
	slow := ArrowFunc(func(ctx context.Context, _ int) (int, error) {
		select {
		case <-time.After(time.Second):
			return 0, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	})

	wrapped := WithTimeout[int, int](50 * time.Millisecond)(slow)
	start := time.Now()
	_, err := wrapped(context.Background(), 1)
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected DeadlineExceeded, got %v", err)
	}
	if elapsed > 200*time.Millisecond {
		t.Errorf("timeout did not fire promptly: elapsed=%v", elapsed)
	}
}

func TestWithTap_DoesNotChangeBehavior(t *testing.T) {
	tapInvocations := 0
	base := ArrowFunc(func(_ context.Context, n int) (int, error) { return n * 3, nil })
	tapped := WithTap[int, int](func(in, out int, err error) {
		tapInvocations++
		if in != 7 || out != 21 {
			t.Errorf("tap saw unexpected (in=%d, out=%d)", in, out)
		}
	})(base)

	got, err := tapped(context.Background(), 7)
	if err != nil || got != 21 {
		t.Errorf("got (%v, %v), want (21, nil)", got, err)
	}
	if tapInvocations != 1 {
		t.Errorf("tap invoked %d times, want 1", tapInvocations)
	}
}
