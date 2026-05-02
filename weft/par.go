package weft

import (
	"context"
	"errors"
	"sync"
)

// Par runs two arrows on the same input concurrently and returns both results
// as a Pair. Errors from both arrows are joined via errors.Join.
func Par[A, B, C any](f Arrow[A, B], g Arrow[A, C]) Arrow[A, Pair[B, C]] {
	return func(ctx context.Context, a A) (Pair[B, C], error) {
		var b B
		var c C
		var bErr, cErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); b, bErr = f(ctx, a) }()
		go func() { defer wg.Done(); c, cErr = g(ctx, a) }()
		wg.Wait()
		if err := errors.Join(bErr, cErr); err != nil {
			return Pair[B, C]{}, err
		}
		return Pair[B, C]{Fst: b, Snd: c}, nil
	}
}

// ParStrict is like Par but cancels the sibling on first error.
func ParStrict[A, B, C any](f Arrow[A, B], g Arrow[A, C]) Arrow[A, Pair[B, C]] {
	return func(ctx context.Context, a A) (Pair[B, C], error) {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		var b B
		var c C
		var bErr, cErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			b, bErr = f(ctx, a)
			if bErr != nil {
				cancel()
			}
		}()
		go func() {
			defer wg.Done()
			c, cErr = g(ctx, a)
			if cErr != nil {
				cancel()
			}
		}()
		wg.Wait()
		if bErr != nil {
			return Pair[B, C]{}, bErr
		}
		if cErr != nil {
			return Pair[B, C]{}, cErr
		}
		return Pair[B, C]{Fst: b, Snd: c}, nil
	}
}

// Par3 runs three arrows on the same input concurrently.
func Par3[A, B, C, D any](
	f Arrow[A, B],
	g Arrow[A, C],
	h Arrow[A, D],
) Arrow[A, Triple[B, C, D]] {
	return func(ctx context.Context, a A) (Triple[B, C, D], error) {
		var b B
		var c C
		var d D
		var bErr, cErr, dErr error
		var wg sync.WaitGroup
		wg.Add(3)
		go func() { defer wg.Done(); b, bErr = f(ctx, a) }()
		go func() { defer wg.Done(); c, cErr = g(ctx, a) }()
		go func() { defer wg.Done(); d, dErr = h(ctx, a) }()
		wg.Wait()
		if err := errors.Join(bErr, cErr, dErr); err != nil {
			return Triple[B, C, D]{}, err
		}
		return Triple[B, C, D]{Fst: b, Snd: c, Thd: d}, nil
	}
}

// Fanout runs two arrows on the same input and returns both results paired.
// Same implementation as Par; the name documents intent.
func Fanout[A, B, C any](f Arrow[A, B], g Arrow[A, C]) Arrow[A, Pair[B, C]] {
	return Par(f, g)
}
