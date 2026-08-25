package object

import (
	"errors"
	"testing"
)

func TestTypedErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		is   func(error) bool
	}{
		{"not found", ErrNotFound, IsNotFound},
		{"conflict", ErrConflict, IsConflict},
		{"quota", ErrQuota, IsQuota},
		{"unavailable", ErrUnavailable, IsUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !tt.is(tt.err) {
				t.Fatalf("expected %v to match its predicate", tt.err)
			}
		})
	}
}

func TestNotFoundErrorWraps(t *testing.T) {
	err := &NotFoundError{Key: "a/b/c"}
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("NotFoundError should unwrap to ErrNotFound")
	}
	if !IsNotFound(err) {
		t.Fatalf("IsNotFound should match NotFoundError")
	}
	if IsConflict(err) {
		t.Fatalf("NotFoundError should not match ErrConflict")
	}
}

func TestConflictErrorWraps(t *testing.T) {
	err := &ConflictError{Key: "a/b/c"}
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("ConflictError should unwrap to ErrConflict")
	}
	if !IsConflict(err) {
		t.Fatalf("IsConflict should match ConflictError")
	}
	if IsNotFound(err) {
		t.Fatalf("ConflictError should not match ErrNotFound")
	}
}

func TestNotFoundErrorDistinct(t *testing.T) {
	// Разные typed-ошибки не должны пересекаться.
	if errors.Is(ErrNotFound, ErrConflict) {
		t.Fatalf("ErrNotFound must not match ErrConflict")
	}
	if errors.Is(ErrQuota, ErrUnavailable) {
		t.Fatalf("ErrQuota must not match ErrUnavailable")
	}
}
