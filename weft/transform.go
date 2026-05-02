package weft

import (
	"context"
	"fmt"
	"time"
)

// Transform wraps an Arrow with cross-cutting behavior (retry, timeout,
// tracing, etc.) without changing its type signature.
//
// In categorical terms, a Transform is a natural transformation on the
// Arrow functor: it modifies how an arrow runs without changing what
// it computes. The defining property is that a Transform takes an
// Arrow[A, B] and returns an Arrow[A, B] with the same observable
// successful behavior but different policy around it.
type Transform[A, B any] func(Arrow[A, B]) Arrow[A, B]

// Apply applies a sequence of transforms to an arrow.
// Transforms are applied left-to-right: the leftmost transform is the
// outermost wrapper. This matches the conventional "middleware"
// reading order.
//
// Example:
//
//	final := Apply(myArrow,
//	    WithOTel("op"),       // outermost (sees retried calls as one span)
//	    WithRetry(3, ...),    // middle
//	    WithTimeout(5*time.Second), // innermost (per-attempt timeout)
//	)
func Apply[A, B any](a Arrow[A, B], transforms ...Transform[A, B]) Arrow[A, B] {
	// Apply right-to-left so the leftmost transform is outermost.
	for i := len(transforms) - 1; i >= 0; i-- {
		a = transforms[i](a)
	}
	return a
}

// BackoffFunc computes the wait duration before the (attempt+1)th retry.
// attempt is 0-indexed: attempt=0 is the first retry.
type BackoffFunc func(attempt int) time.Duration

// LinearBackoff returns a constant backoff between retries.
func LinearBackoff(d time.Duration) BackoffFunc {
	return func(_ int) time.Duration { return d }
}

// ExponentialBackoff returns a backoff that doubles each attempt,
// starting at the given base duration.
func ExponentialBackoff(base time.Duration) BackoffFunc {
	return func(attempt int) time.Duration {
		return base << attempt // base * 2^attempt
	}
}

// WithRetry retries the arrow up to maxAttempts times on transient errors.
//
// "Transient" means errors classified as ClassTransient by the package's
// Classify function, OR any error if the underlying arrow doesn't
// produce ArrowError values. This conservative default means retry
// applies to most errors out of the box; seams that classify their
// errors get more precise behavior automatically.
//
// Cancellation is honored: if the context is cancelled during the
// backoff sleep, the retry stops and returns ctx.Err().
func WithRetry[A, B any](maxAttempts int, backoff BackoffFunc) Transform[A, B] {
	return func(inner Arrow[A, B]) Arrow[A, B] {
		return func(ctx context.Context, a A) (B, error) {
			var (
				b       B
				lastErr error
			)
			for attempt := 0; attempt < maxAttempts; attempt++ {
				b, lastErr = inner(ctx, a)
				if lastErr == nil {
					return b, nil
				}
				// Don't retry on permanent errors or cancellation.
				class := Classify(lastErr)
				if class == ClassPermanent || class == ClassUserCancelled {
					return b, lastErr
				}
				if ctx.Err() != nil {
					return b, lastErr
				}
				// Last attempt? Don't sleep, just return.
				if attempt == maxAttempts-1 {
					break
				}
				select {
				case <-ctx.Done():
					return b, ctx.Err()
				case <-time.After(backoff(attempt)):
				}
			}
			return b, fmt.Errorf("retry exhausted (%d attempts): %w", maxAttempts, lastErr)
		}
	}
}

// WithTimeout enforces a maximum duration for the arrow.
// If the arrow does not complete in time, the context is cancelled
// and the resulting error wraps context.DeadlineExceeded.
func WithTimeout[A, B any](d time.Duration) Transform[A, B] {
	return func(inner Arrow[A, B]) Arrow[A, B] {
		return func(ctx context.Context, a A) (B, error) {
			ctx, cancel := context.WithTimeout(ctx, d)
			defer cancel()
			return inner(ctx, a)
		}
	}
}

// WithTap runs a side-effecting function on every (input, output, error)
// triple without changing the arrow's behavior. Use for logging, metrics,
// or any observability concern that doesn't need to participate in the
// arrow's result.
func WithTap[A, B any](tap func(in A, out B, err error)) Transform[A, B] {
	return func(inner Arrow[A, B]) Arrow[A, B] {
		return func(ctx context.Context, a A) (B, error) {
			b, err := inner(ctx, a)
			tap(a, b, err)
			return b, err
		}
	}
}
