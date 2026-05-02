package weft

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"testing/quick"
	"time"
)

// These tests verify the categorical laws that the weft package must
// satisfy. They are the package's specification: every change to the
// algebra must keep these passing.

// arrowEquiv tests that two arrows produce the same (output, error)
// for the same inputs across many random samples.
func arrowEquiv[A, B comparable](
	t *testing.T,
	f, g Arrow[A, B],
	desc string,
) {
	t.Helper()
	prop := func(a A) bool {
		ctx := context.Background()
		b1, err1 := f(ctx, a)
		b2, err2 := g(ctx, a)
		switch {
		case err1 == nil && err2 == nil:
			return b1 == b2
		case err1 != nil && err2 != nil:
			return err1.Error() == err2.Error()
		default:
			return false
		}
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 200}); err != nil {
		t.Errorf("%s: %v", desc, err)
	}
}

// === Identity laws ============================================================

func TestIdentityLaw_Left(t *testing.T) {
	f := ArrowFunc(func(_ context.Context, n int) (int, error) {
		return n*2 + 1, nil
	})
	arrowEquiv(t, Compose(Id[int](), f), f, "Compose(Id, f) ≡ f")
}

func TestIdentityLaw_Right(t *testing.T) {
	f := ArrowFunc(func(_ context.Context, n int) (int, error) {
		return n*2 + 1, nil
	})
	arrowEquiv(t, Compose(f, Id[int]()), f, "Compose(f, Id) ≡ f")
}

func TestIdentityLaw_OnFailingArrow(t *testing.T) {
	f := ArrowFunc(func(_ context.Context, n int) (int, error) {
		if n%3 == 0 {
			return 0, fmt.Errorf("rejecting multiple of 3: %d", n)
		}
		return n * 2, nil
	})
	arrowEquiv(t, Compose(Id[int](), f), f, "left identity with failures")
	arrowEquiv(t, Compose(f, Id[int]()), f, "right identity with failures")
}

// === Associativity ============================================================

func TestAssociativityLaw(t *testing.T) {
	f := ArrowFunc(func(_ context.Context, n int) (int, error) { return n + 1, nil })
	g := ArrowFunc(func(_ context.Context, n int) (int, error) { return n * 2, nil })
	h := ArrowFunc(func(_ context.Context, n int) (int, error) { return n - 3, nil })

	arrowEquiv(t,
		Compose(Compose(f, g), h),
		Compose(f, Compose(g, h)),
		"associativity of Compose")
}

func TestAssociativityLaw_WithErrors(t *testing.T) {
	f := ArrowFunc(func(_ context.Context, n int) (int, error) {
		if n < 0 {
			return 0, errors.New("f rejects negative")
		}
		return n + 1, nil
	})
	g := ArrowFunc(func(_ context.Context, n int) (int, error) {
		if n > 100 {
			return 0, errors.New("g rejects > 100")
		}
		return n * 2, nil
	})
	h := ArrowFunc(func(_ context.Context, n int) (int, error) {
		if n == 42 {
			return 0, errors.New("h rejects 42")
		}
		return n - 3, nil
	})

	arrowEquiv(t,
		Compose(Compose(f, g), h),
		Compose(f, Compose(g, h)),
		"associativity with mixed errors")
}

// === Pipe equivalence =========================================================

func TestPipe3_EquivalentToNestedCompose(t *testing.T) {
	f := ArrowFunc(func(_ context.Context, n int) (int, error) { return n + 1, nil })
	g := ArrowFunc(func(_ context.Context, n int) (int, error) { return n * 2, nil })
	h := ArrowFunc(func(_ context.Context, n int) (int, error) { return n - 3, nil })

	arrowEquiv(t, Pipe3(f, g, h), Compose(Compose(f, g), h),
		"Pipe3 ≡ nested Compose")
}

func TestPipe4_EquivalentToNestedCompose(t *testing.T) {
	f := ArrowFunc(func(_ context.Context, n int) (int, error) { return n + 1, nil })
	g := ArrowFunc(func(_ context.Context, n int) (int, error) { return n * 2, nil })
	h := ArrowFunc(func(_ context.Context, n int) (int, error) { return n - 3, nil })
	i := ArrowFunc(func(_ context.Context, n int) (int, error) { return n + 7, nil })

	arrowEquiv(t, Pipe4(f, g, h, i),
		Compose(Compose(Compose(f, g), h), i),
		"Pipe4 ≡ nested Compose")
}

// === Map functor laws =========================================================

func TestMap_PreservesIdentity(t *testing.T) {
	a := ArrowFunc(func(_ context.Context, n int) (int, error) { return n * 2, nil })
	identity := func(n int) int { return n }
	arrowEquiv(t, Map(a, identity), a, "Map(a, id) ≡ a")
}

func TestMap_PreservesComposition(t *testing.T) {
	a := ArrowFunc(func(_ context.Context, n int) (int, error) { return n * 2, nil })
	f := func(n int) int { return n + 1 }
	g := func(n int) int { return n * 3 }

	arrowEquiv(t,
		Map(a, func(n int) int { return g(f(n)) }),
		Map(Map(a, f), g),
		"Map preserves composition")
}

func TestMap_EquivalentToComposeWithPure(t *testing.T) {
	a := ArrowFunc(func(_ context.Context, n int) (int, error) { return n * 2, nil })
	f := func(n int) int { return n + 1 }
	arrowEquiv(t, Map(a, f), Compose(a, Pure(f)), "Map ≡ Compose with Pure")
}

// === Cancellation propagation ================================================

func TestCompose_PropagatesCancellation(t *testing.T) {
	called := atomic.Int32{}
	f := ArrowFunc(func(ctx context.Context, n int) (int, error) {
		called.Add(1)
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		return n + 1, nil
	})
	g := ArrowFunc(func(ctx context.Context, n int) (int, error) {
		called.Add(1)
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		return n * 2, nil
	})

	composed := Compose(f, g)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := composed(ctx, 1)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	if called.Load() > 1 {
		t.Errorf("g should not have run after f returned cancellation, called=%d", called.Load())
	}
}

// === Par / Fanout =============================================================

func TestPar_BothSucceed(t *testing.T) {
	f := ArrowFunc(func(_ context.Context, n int) (int, error) { return n * 2, nil })
	g := ArrowFunc(func(_ context.Context, n int) (string, error) { return "hi", nil })
	got, err := Par(f, g)(context.Background(), 21)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Fst != 42 || got.Snd != "hi" {
		t.Errorf("got %+v, want {42, hi}", got)
	}
}

func TestPar_RunsConcurrently(t *testing.T) {
	delay := 50 * time.Millisecond
	f := ArrowFunc(func(_ context.Context, _ struct{}) (int, error) {
		time.Sleep(delay)
		return 1, nil
	})
	g := ArrowFunc(func(_ context.Context, _ struct{}) (int, error) {
		time.Sleep(delay)
		return 2, nil
	})

	start := time.Now()
	_, err := Par(f, g)(context.Background(), struct{}{})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed > delay*2-10*time.Millisecond {
		t.Errorf("Par did not run concurrently: elapsed=%v", elapsed)
	}
}

func TestParStrict_OneFailsCancelsOther(t *testing.T) {
	gCancelled := atomic.Bool{}
	failFast := ArrowFunc(func(_ context.Context, _ int) (int, error) {
		return 0, errors.New("immediate failure")
	})
	slow := ArrowFunc(func(ctx context.Context, _ int) (int, error) {
		select {
		case <-time.After(500 * time.Millisecond):
			return 0, nil
		case <-ctx.Done():
			gCancelled.Store(true)
			return 0, ctx.Err()
		}
	})

	_, err := ParStrict(failFast, slow)(context.Background(), 1)
	if err == nil {
		t.Fatal("expected error from failed branch")
	}
	if !gCancelled.Load() {
		t.Error("slow branch should have been cancelled when fast branch failed")
	}
}

// === Sum ======================================================================

func TestSum_DispatchesLeft(t *testing.T) {
	leftCalled := atomic.Bool{}
	rightCalled := atomic.Bool{}
	left := ArrowFunc(func(_ context.Context, n int) (string, error) {
		leftCalled.Store(true)
		return "left", nil
	})
	right := ArrowFunc(func(_ context.Context, s string) (string, error) {
		rightCalled.Store(true)
		return "right", nil
	})

	got, err := Sum(left, right)(context.Background(), Left[int, string](42))
	if err != nil || got != "left" {
		t.Errorf("got (%v, %v), want (left, nil)", got, err)
	}
	if !leftCalled.Load() {
		t.Error("left branch should have been called")
	}
	if rightCalled.Load() {
		t.Error("right branch should NOT have been called")
	}
}

func TestSum_DispatchesRight(t *testing.T) {
	left := ArrowFunc(func(_ context.Context, n int) (string, error) { return "left", nil })
	right := ArrowFunc(func(_ context.Context, s string) (string, error) { return "right:" + s, nil })

	got, err := Sum(left, right)(context.Background(), Right[int, string]("hi"))
	if err != nil || got != "right:hi" {
		t.Errorf("got (%v, %v), want (right:hi, nil)", got, err)
	}
}

func TestSum_WithIdAsBranch(t *testing.T) {
	work := ArrowFunc(func(_ context.Context, n int) (int, error) { return n * 100, nil })
	dispatch := Sum(Id[int](), work)

	left, _ := dispatch(context.Background(), Left[int, int](7))
	right, _ := dispatch(context.Background(), Right[int, int](7))
	if left != 7 {
		t.Errorf("Id branch: got %d, want 7", left)
	}
	if right != 700 {
		t.Errorf("work branch: got %d, want 700", right)
	}
}

// === Fallback =================================================================

func TestFallback_PrimarySucceeds(t *testing.T) {
	backupCalled := atomic.Bool{}
	primary := ArrowFunc(func(_ context.Context, n int) (int, error) { return n * 2, nil })
	backup := ArrowFunc(func(_ context.Context, n int) (int, error) {
		backupCalled.Store(true)
		return 0, nil
	})

	got, err := Fallback(primary, backup)(context.Background(), 21)
	if err != nil || got != 42 {
		t.Errorf("got (%v, %v), want (42, nil)", got, err)
	}
	if backupCalled.Load() {
		t.Error("backup should not have been called")
	}
}

func TestFallback_BackupRunsOnPrimaryFailure(t *testing.T) {
	primary := ArrowFunc(func(_ context.Context, n int) (int, error) {
		return 0, errors.New("primary failed")
	})
	backup := ArrowFunc(func(_ context.Context, n int) (int, error) { return n * 2, nil })

	got, err := Fallback(primary, backup)(context.Background(), 21)
	if err != nil || got != 42 {
		t.Errorf("got (%v, %v), want (42, nil)", got, err)
	}
}

func TestFallback_DoesNotRecoverFromCancellation(t *testing.T) {
	backupCalled := atomic.Bool{}
	primary := ArrowFunc(func(ctx context.Context, n int) (int, error) {
		<-ctx.Done()
		return 0, ctx.Err()
	})
	backup := ArrowFunc(func(_ context.Context, n int) (int, error) {
		backupCalled.Store(true)
		return n * 2, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Fallback(primary, backup)(ctx, 21)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected cancellation to propagate, got %v", err)
	}
	if backupCalled.Load() {
		t.Error("backup should not run after user cancellation")
	}
}
