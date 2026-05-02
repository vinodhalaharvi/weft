// Package weft provides a category-theoretic algebra for composing
// effectful, cancellable computations.
//
// The central type is Arrow[A, B], which represents a morphism in a
// Kleisli-like category: a function from A to B that is effectful
// (returns an error), cancellable (takes a context.Context), and
// type-preserving under composition.
//
// The design's load-bearing claim is that almost any "thing that does
// work" — a remote API call, a local function, an LLM invocation, an
// MCP tool, a multi-step pipeline, an agent loop — can be expressed as
// an Arrow, and once it is, the same combinators (Compose, Par, Sum,
// Map, Traverse) operate uniformly on it without caring how it was
// constructed. This is the role-erasure property the package exists
// to provide.
package weft

import "context"

// Arrow is a morphism from A to B with explicit context and error.
type Arrow[A, B any] func(ctx context.Context, a A) (B, error)

// Id is the identity arrow: it returns its input unchanged.
func Id[A any]() Arrow[A, A] {
	return func(_ context.Context, a A) (A, error) { return a, nil }
}

// ArrowFunc is a small adapter so plain functions of the right shape
// can be used in places that want an Arrow value explicitly.
func ArrowFunc[A, B any](f func(ctx context.Context, a A) (B, error)) Arrow[A, B] {
	return f
}

// Pure lifts a pure (non-effectful, non-failing) function into an Arrow.
func Pure[A, B any](f func(A) B) Arrow[A, B] {
	return func(_ context.Context, a A) (B, error) { return f(a), nil }
}
