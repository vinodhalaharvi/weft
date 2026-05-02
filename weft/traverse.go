package weft

import (
	"context"
	"errors"
	"sync"
)

// ErrorPolicy controls how Traverse handles per-item failures.
type ErrorPolicy int

const (
	FailFast ErrorPolicy = iota
	CollectErrors
	SkipFailures
	PartialResults
)

// TraverseOpt configures a Traverse call.
type TraverseOpt func(*traverseConfig)

type traverseConfig struct {
	concurrency int
	policy      ErrorPolicy
}

// WithConcurrency sets the maximum concurrency for Traverse.
func WithConcurrency(n int) TraverseOpt {
	return func(c *traverseConfig) {
		if n < 1 {
			n = 1
		}
		c.concurrency = n
	}
}

// OnError sets the error policy for Traverse.
func OnError(p ErrorPolicy) TraverseOpt {
	return func(c *traverseConfig) { c.policy = p }
}

// Traverse lifts an Arrow[A, B] to an Arrow[[]A, []B] with bounded concurrency.
func Traverse[A, B any](f Arrow[A, B], opts ...TraverseOpt) Arrow[[]A, []B] {
	cfg := traverseConfig{concurrency: 1, policy: FailFast}
	for _, o := range opts {
		o(&cfg)
	}
	return func(ctx context.Context, as []A) ([]B, error) {
		n := len(as)
		results := make([]B, n)
		errs := make([]error, n)

		sem := make(chan struct{}, cfg.concurrency)
		var wg sync.WaitGroup

		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		for i := range as {
			i := i
			wg.Add(1)
			go func() {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					errs[i] = ctx.Err()
					return
				}
				defer func() { <-sem }()

				if ctx.Err() != nil {
					errs[i] = ctx.Err()
					return
				}
				b, err := f(ctx, as[i])
				// Always store the result, even if there was an error.
				// This lets callers return both a partial result AND an
				// error from the inner arrow — useful when the result
				// itself carries diagnostic information (e.g., a path
				// or ID that should be reported in the failure case).
				results[i] = b
				if err != nil {
					errs[i] = err
					if cfg.policy == FailFast {
						cancel()
					}
					return
				}
			}()
		}
		wg.Wait()

		failures := make(map[int]error)
		var firstErr error
		for i, e := range errs {
			if e != nil {
				failures[i] = e
				if firstErr == nil {
					firstErr = e
				}
			}
		}

		if len(failures) == 0 {
			return results, nil
		}

		switch cfg.policy {
		case FailFast:
			return nil, firstErr
		case CollectErrors:
			joined := make([]error, 0, len(failures))
			for _, e := range errs {
				if e != nil {
					joined = append(joined, e)
				}
			}
			return results, errors.Join(joined...)
		case SkipFailures:
			out := make([]B, 0, n-len(failures))
			for i, e := range errs {
				if e == nil {
					out = append(out, results[i])
				}
			}
			return out, nil
		case PartialResults:
			return results, &PartialError{Failures: failures, Total: n}
		default:
			return nil, firstErr
		}
	}
}
