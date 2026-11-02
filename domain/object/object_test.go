package object

import (
	"errors"
	"fmt"
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
		{"forbidden", ErrForbidden, IsForbidden},
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

// TestIsUnsafePath — предикат ErrUnsafePath (в том числе обёрнутой ошибки).
func TestIsUnsafePath(t *testing.T) {
	if !IsUnsafePath(ErrUnsafePath) {
		t.Fatal("IsUnsafePath(ErrUnsafePath) = false, want true")
	}
	wrapped := fmt.Errorf("adapter: %w", ErrUnsafePath)
	if !IsUnsafePath(wrapped) {
		t.Fatalf("IsUnsafePath(wrapped) = false, want true")
	}
	if IsUnsafePath(ErrNotFound) {
		t.Fatal("IsUnsafePath(ErrNotFound) = true, want false")
	}
}

// TestObjectKeyString — строковое представление ключа.
func TestObjectKeyString(t *testing.T) {
	k := ObjectKey("thumbs/photo-jpg/200x200.webp")
	if got := k.String(); got != "thumbs/photo-jpg/200x200.webp" {
		t.Errorf("String() = %q", got)
	}
	if got := ObjectKey("").String(); got != "" {
		t.Errorf("String(empty) = %q, want empty", got)
	}
}

// TestErrorMessages — тексты типизированных ошибок содержат контекст
// (ключ, причину, лимиты), чтобы диагностика не требовала разбора типа.
func TestErrorMessages(t *testing.T) {
	t.Run("not found", func(t *testing.T) {
		got := (&NotFoundError{Key: "a/b"}).Error()
		want := `object "a/b" not found`
		if got != want {
			t.Errorf("Error() = %q, want %q", got, want)
		}
	})

	t.Run("conflict", func(t *testing.T) {
		got := (&ConflictError{Key: "a/b"}).Error()
		want := `object "a/b" already exists`
		if got != want {
			t.Errorf("Error() = %q, want %q", got, want)
		}
	})

	t.Run("quota minimal", func(t *testing.T) {
		got := (&QuotaError{}).Error()
		if got != "storage quota exceeded" {
			t.Errorf("Error() = %q, want %q", got, "storage quota exceeded")
		}
	})

	t.Run("quota full", func(t *testing.T) {
		err := &QuotaError{
			Key:     "a/b",
			Limit:   100,
			Current: 150,
			Reason:  "max bytes",
			Err:     errors.New("ENOSPC"),
		}
		want := `storage quota exceeded: max bytes (key a/b) (limit=100 current=150): ENOSPC`
		if got := err.Error(); got != want {
			t.Errorf("Error() = %q, want %q", got, want)
		}
		if !errors.Is(err, ErrQuota) {
			t.Error("errors.Is(quota, ErrQuota) = false")
		}
		if !IsQuota(err) {
			t.Error("IsQuota(quota) = false")
		}
	})

	t.Run("quota reason only", func(t *testing.T) {
		got := (&QuotaError{Reason: "objects"}).Error()
		want := "storage quota exceeded: objects"
		if got != want {
			t.Errorf("Error() = %q, want %q", got, want)
		}
	})
}

// TestQuotaErrorUnwrap — Unwrap возвращает ErrQuota и внутреннюю причину
// (если задана), поэтому errors.Is работает для обеих.
func TestQuotaErrorUnwrap(t *testing.T) {
	// Без причины: только ErrQuota.
	plain := &QuotaError{Reason: "max bytes"}
	if !errors.Is(plain, ErrQuota) {
		t.Fatal("plain QuotaError must unwrap to ErrQuota")
	}
	if n := len(plain.Unwrap()); n != 1 {
		t.Errorf("len(Unwrap()) = %d, want 1", n)
	}

	// С причиной: и ErrQuota, и причина.
	cause := errors.New("disk full")
	withCause := &QuotaError{Err: cause}
	if !errors.Is(withCause, ErrQuota) {
		t.Fatal("QuotaError must unwrap to ErrQuota")
	}
	if !errors.Is(withCause, cause) {
		t.Fatal("QuotaError must unwrap to the underlying cause")
	}
	if n := len(withCause.Unwrap()); n != 2 {
		t.Errorf("len(Unwrap()) = %d, want 2", n)
	}
}
