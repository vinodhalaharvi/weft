package weft

import (
	"context"
	"testing"
	"time"
)

// These tests cover public API surfaces that the law/example tests
// don't exercise. They're correctness checks, not law verification.

func TestPar3_AllSucceed(t *testing.T) {
	f := ArrowFunc(func(_ context.Context, n int) (int, error) { return n + 1, nil })
	g := ArrowFunc(func(_ context.Context, n int) (string, error) { return "g", nil })
	h := ArrowFunc(func(_ context.Context, n int) (bool, error) { return n > 0, nil })

	got, err := Par3(f, g, h)(context.Background(), 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Fst != 6 || got.Snd != "g" || got.Thd != true {
		t.Errorf("got %+v, want {6 g true}", got)
	}
}

func TestFanout_RunsBothBranches(t *testing.T) {
	f := ArrowFunc(func(_ context.Context, n int) (int, error) { return n * 2, nil })
	g := ArrowFunc(func(_ context.Context, n int) (int, error) { return n + 100, nil })

	got, err := Fanout(f, g)(context.Background(), 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Fst != 10 || got.Snd != 105 {
		t.Errorf("got %+v, want {10 105}", got)
	}
}

func TestSum4_AllBranches(t *testing.T) {
	fa := ArrowFunc(func(_ context.Context, n int) (string, error) { return "A", nil })
	fb := ArrowFunc(func(_ context.Context, s string) (string, error) { return "B", nil })
	fc := ArrowFunc(func(_ context.Context, b bool) (string, error) { return "C", nil })
	fd := ArrowFunc(func(_ context.Context, f float64) (string, error) { return "D", nil })

	dispatch := Sum4(fa, fb, fc, fd)

	cases := []struct {
		name string
		in   FourWay[int, string, bool, float64]
		want string
	}{
		{"A", FourWay[int, string, bool, float64]{A: ptr(1)}, "A"},
		{"B", FourWay[int, string, bool, float64]{B: ptr("x")}, "B"},
		{"C", FourWay[int, string, bool, float64]{C: ptr(true)}, "C"},
		{"D", FourWay[int, string, bool, float64]{D: ptr(3.14)}, "D"},
	}
	for _, tc := range cases {
		got, err := dispatch(context.Background(), tc.in)
		if err != nil {
			t.Errorf("%s: unexpected error: %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestSum4_EmptyErrors(t *testing.T) {
	fa := ArrowFunc(func(_ context.Context, n int) (string, error) { return "A", nil })
	fb := ArrowFunc(func(_ context.Context, s string) (string, error) { return "B", nil })
	fc := ArrowFunc(func(_ context.Context, b bool) (string, error) { return "C", nil })
	fd := ArrowFunc(func(_ context.Context, f float64) (string, error) { return "D", nil })

	dispatch := Sum4(fa, fb, fc, fd)
	_, err := dispatch(context.Background(), FourWay[int, string, bool, float64]{})
	if err != ErrEmptyFourWay {
		t.Errorf("got %v, want ErrEmptyFourWay", err)
	}
}

func TestMakePair(t *testing.T) {
	p := MakePair(42, "hello")
	if p.Fst != 42 || p.Snd != "hello" {
		t.Errorf("got %+v, want {42 hello}", p)
	}
}

func TestFstSnd_Projections(t *testing.T) {
	p := MakePair(7, "world")
	a, err := Fst[int, string]()(context.Background(), p)
	if err != nil || a != 7 {
		t.Errorf("Fst: got (%v, %v), want (7, nil)", a, err)
	}
	b, err := Snd[int, string]()(context.Background(), p)
	if err != nil || b != "world" {
		t.Errorf("Snd: got (%v, %v), want (world, nil)", b, err)
	}
}

func TestExponentialBackoff(t *testing.T) {
	bf := ExponentialBackoff(10 * time.Millisecond)
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 10 * time.Millisecond},
		{1, 20 * time.Millisecond},
		{2, 40 * time.Millisecond},
		{3, 80 * time.Millisecond},
	}
	for _, tc := range cases {
		got := bf(tc.attempt)
		if got != tc.want {
			t.Errorf("attempt %d: got %v, want %v", tc.attempt, got, tc.want)
		}
	}
}

func TestPure_LiftsFunction(t *testing.T) {
	double := Pure(func(n int) int { return n * 2 })
	got, err := double(context.Background(), 21)
	if err != nil || got != 42 {
		t.Errorf("got (%v, %v), want (42, nil)", got, err)
	}
}

func TestPreMap_TransformsInput(t *testing.T) {
	parseInt := func(s string) int { return len(s) }
	doubler := ArrowFunc(func(_ context.Context, n int) (int, error) { return n * 2, nil })
	combined := PreMap(parseInt, doubler)
	got, err := combined(context.Background(), "hello")
	if err != nil || got != 10 {
		t.Errorf("got (%v, %v), want (10, nil)", got, err)
	}
}

func TestArrowError_Format(t *testing.T) {
	e := &ArrowError{
		Class: ClassTransient,
		Op:    "test.op",
		Cause: errString("inner"),
	}
	msg := e.Error()
	if !contains(msg, "test.op") || !contains(msg, "transient") {
		t.Errorf("error message missing expected fields: %q", msg)
	}
}

func TestClassify(t *testing.T) {
	if Classify(nil) != ClassUnknown {
		t.Error("nil should classify as Unknown")
	}
	plain := errString("plain error")
	if Classify(plain) != ClassUnknown {
		t.Error("plain error should classify as Unknown")
	}
	wrapped := &ArrowError{Class: ClassTransient, Cause: plain}
	if Classify(wrapped) != ClassTransient {
		t.Error("ArrowError should expose its class via Classify")
	}
}

// === helpers ==================================================================

func ptr[T any](v T) *T { return &v }

type errString string

func (e errString) Error() string { return string(e) }

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
