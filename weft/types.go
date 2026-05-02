package weft

import "context"

// Pair is the categorical product of two types.
//
// We use a struct rather than a generic tuple type because Go has no
// tuple syntax; named fields read better at use-sites than .Item1 /
// .Item2 conventions from other languages.
type Pair[A, B any] struct {
	Fst A
	Snd B
}

// MakePair is a tiny constructor sugar.
func MakePair[A, B any](a A, b B) Pair[A, B] { return Pair[A, B]{Fst: a, Snd: b} }

// Fst projects the first element of a pair as an Arrow.
//
// Useful as one half of Fanout when you want to preserve a value
// alongside an arrow's output.
func Fst[A, B any]() Arrow[Pair[A, B], A] {
	return func(_ context.Context, p Pair[A, B]) (A, error) { return p.Fst, nil }
}

// Snd projects the second element of a pair as an Arrow.
func Snd[A, B any]() Arrow[Pair[A, B], B] {
	return func(_ context.Context, p Pair[A, B]) (B, error) { return p.Snd, nil }
}

// Triple is the 3-tuple analogue of Pair.
type Triple[A, B, C any] struct {
	Fst A
	Snd B
	Thd C
}

// Either is a tagged union of two types: it holds an A or a B, never
// both. Exactly one of Left or Right is non-nil.
//
// Either is the categorical coproduct, used in this package primarily
// for routing.
type Either[A, B any] struct {
	Left  *A
	Right *B
}

// Left constructs an Either holding an A.
func Left[A, B any](a A) Either[A, B] { return Either[A, B]{Left: &a} }

// Right constructs an Either holding a B.
func Right[A, B any](b B) Either[A, B] { return Either[A, B]{Right: &b} }

// IsLeft reports whether the Either holds an A.
func (e Either[A, B]) IsLeft() bool { return e.Left != nil }

// IsRight reports whether the Either holds a B.
func (e Either[A, B]) IsRight() bool { return e.Right != nil }
