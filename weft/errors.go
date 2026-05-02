package weft

import (
	"errors"
	"fmt"
)

// Class classifies an error for combinators that decide retry/fallback behavior.
type Class int

const (
	ClassUnknown Class = iota
	ClassTransient
	ClassPermanent
	ClassBudget
	ClassUserCancelled
)

func (c Class) String() string {
	switch c {
	case ClassTransient:
		return "transient"
	case ClassPermanent:
		return "permanent"
	case ClassBudget:
		return "budget"
	case ClassUserCancelled:
		return "user-cancelled"
	default:
		return "unknown"
	}
}

// ArrowError is the framework's structured error type.
type ArrowError struct {
	Class    Class
	Op       string
	Cause    error
	Metadata map[string]any
}

func (e *ArrowError) Error() string {
	if e == nil {
		return "<nil ArrowError>"
	}
	if e.Cause == nil {
		return fmt.Sprintf("%s: %s", e.Op, e.Class)
	}
	return fmt.Sprintf("%s [%s]: %v", e.Op, e.Class, e.Cause)
}

func (e *ArrowError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// Classify extracts the class of an error.
func Classify(err error) Class {
	if err == nil {
		return ClassUnknown
	}
	var ae *ArrowError
	if errors.As(err, &ae) {
		return ae.Class
	}
	return ClassUnknown
}

// Sentinel errors.
var (
	ErrEmptyEither    = errors.New("weft: empty Either passed to Sum")
	ErrEmptyFourWay   = errors.New("weft: empty FourWay passed to Sum4")
	ErrTraverseFailed = errors.New("weft: traverse encountered failures")
)

// PartialError is returned by Traverse with PartialResults policy.
type PartialError struct {
	Failures map[int]error
	Total    int
}

func (p *PartialError) Error() string {
	return fmt.Sprintf("traverse: %d of %d items failed", len(p.Failures), p.Total)
}

func (p *PartialError) Unwrap() error {
	return ErrTraverseFailed
}
