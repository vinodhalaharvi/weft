package weft

import "context"

// Sum is the categorical coproduct as an arrow combinator.
func Sum[A, B, C any](f Arrow[A, C], g Arrow[B, C]) Arrow[Either[A, B], C] {
	return func(ctx context.Context, e Either[A, B]) (C, error) {
		var zero C
		switch {
		case e.IsLeft():
			return f(ctx, *e.Left)
		case e.IsRight():
			return g(ctx, *e.Right)
		default:
			return zero, ErrEmptyEither
		}
	}
}

// FourWay is a 4-way coproduct.
type FourWay[A, B, C, D any] struct {
	A *A
	B *B
	C *C
	D *D
}

// Sum4 dispatches across four branches.
func Sum4[A, B, C, D, R any](
	fa Arrow[A, R],
	fb Arrow[B, R],
	fc Arrow[C, R],
	fd Arrow[D, R],
) Arrow[FourWay[A, B, C, D], R] {
	return func(ctx context.Context, w FourWay[A, B, C, D]) (R, error) {
		var zero R
		switch {
		case w.A != nil:
			return fa(ctx, *w.A)
		case w.B != nil:
			return fb(ctx, *w.B)
		case w.C != nil:
			return fc(ctx, *w.C)
		case w.D != nil:
			return fd(ctx, *w.D)
		default:
			return zero, ErrEmptyFourWay
		}
	}
}

// Fallback tries the primary; on error, falls back to the backup.
func Fallback[A, B any](primary, backup Arrow[A, B]) Arrow[A, B] {
	return func(ctx context.Context, a A) (B, error) {
		b, err := primary(ctx, a)
		if err == nil {
			return b, nil
		}
		if ctx.Err() != nil {
			return b, err
		}
		return backup(ctx, a)
	}
}

// OnSentinel falls back only on a specific sentinel error.
func OnSentinel[A, B any](sentinel error, backup Arrow[A, B]) func(Arrow[A, B]) Arrow[A, B] {
	return func(primary Arrow[A, B]) Arrow[A, B] {
		return func(ctx context.Context, a A) (B, error) {
			b, err := primary(ctx, a)
			if err == nil {
				return b, nil
			}
			if !errIs(err, sentinel) {
				return b, err
			}
			return backup(ctx, a)
		}
	}
}

func errIs(err, target error) bool {
	if err == nil || target == nil {
		return err == target
	}
	for {
		if err == target {
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
		if err == nil {
			return false
		}
	}
}
