package weft

import "context"

// Compose chains two arrows: the output of f feeds the input of g.
//
// Compose satisfies the categorical laws:
//   - Compose(Id, f) == f                                    (left identity)
//   - Compose(f, Id) == f                                    (right identity)
//   - Compose(Compose(f, g), h) == Compose(f, Compose(g, h)) (associativity)
//
// These laws are verified in laws_test.go.
func Compose[A, B, C any](f Arrow[A, B], g Arrow[B, C]) Arrow[A, C] {
	return func(ctx context.Context, a A) (C, error) {
		var zero C
		b, err := f(ctx, a)
		if err != nil {
			return zero, err
		}
		return g(ctx, b)
	}
}

// Pipe2 is sugar for Compose, exposed at the user-facing call surface.
func Pipe2[A, B, C any](f Arrow[A, B], g Arrow[B, C]) Arrow[A, C] {
	return Compose(f, g)
}

// Pipe3 chains three arrows.
func Pipe3[A, B, C, D any](
	f Arrow[A, B],
	g Arrow[B, C],
	h Arrow[C, D],
) Arrow[A, D] {
	return Compose(Compose(f, g), h)
}

// Pipe4 chains four arrows.
func Pipe4[A, B, C, D, E any](
	f Arrow[A, B],
	g Arrow[B, C],
	h Arrow[C, D],
	i Arrow[D, E],
) Arrow[A, E] {
	return Compose(Pipe3(f, g, h), i)
}

// Pipe5 chains five arrows.
func Pipe5[A, B, C, D, E, F any](
	f1 Arrow[A, B],
	f2 Arrow[B, C],
	f3 Arrow[C, D],
	f4 Arrow[D, E],
	f5 Arrow[E, F],
) Arrow[A, F] {
	return Compose(Pipe4(f1, f2, f3, f4), f5)
}

// Pipe6 chains six arrows.
func Pipe6[A, B, C, D, E, F, G any](
	f1 Arrow[A, B],
	f2 Arrow[B, C],
	f3 Arrow[C, D],
	f4 Arrow[D, E],
	f5 Arrow[E, F],
	f6 Arrow[F, G],
) Arrow[A, G] {
	return Compose(Pipe5(f1, f2, f3, f4, f5), f6)
}

// Map applies a pure function to the output of an arrow.
func Map[A, B, C any](f Arrow[A, B], h func(B) C) Arrow[A, C] {
	return func(ctx context.Context, a A) (C, error) {
		var zero C
		b, err := f(ctx, a)
		if err != nil {
			return zero, err
		}
		return h(b), nil
	}
}

// PreMap applies a pure function to the input of an arrow.
func PreMap[A, B, C any](h func(A) B, f Arrow[B, C]) Arrow[A, C] {
	return func(ctx context.Context, a A) (C, error) {
		return f(ctx, h(a))
	}
}
