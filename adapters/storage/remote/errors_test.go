package remote

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"gitverse.ru/pkg-ru/imager/domain/object"
)

func TestMapError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want error
	}{
		{"nil", nil, nil},
		{"not found passthrough", object.ErrNotFound, object.ErrNotFound},
		{"conflict passthrough", object.ErrConflict, object.ErrConflict},
		{"quota passthrough", object.ErrQuota, object.ErrQuota},
		{"unsafe passthrough", object.ErrUnsafePath, object.ErrUnsafePath},
		{"unavailable passthrough", object.ErrUnavailable, object.ErrUnavailable},
		{"canceled passthrough", context.Canceled, context.Canceled},
		{"deadline passthrough", context.DeadlineExceeded, context.DeadlineExceeded},
		{"generic maps to unavailable", errors.New("boom"), object.ErrUnavailable},
		{"wrapped generic maps to unavailable", fmt.Errorf("wrap: %w", errors.New("boom")), object.ErrUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MapError("op", tt.err)
			if tt.want == nil {
				if got != nil {
					t.Fatalf("expected nil, got %v", got)
				}
				return
			}
			if !errors.Is(got, tt.want) {
				t.Fatalf("expected %v, got %v", tt.want, got)
			}
		})
	}
}

func TestNotFoundConflict(t *testing.T) {
	if !errors.Is(NotFound("k"), object.ErrNotFound) {
		t.Fatal("NotFound should wrap ErrNotFound")
	}
	if !errors.Is(Conflict("k"), object.ErrConflict) {
		t.Fatal("Conflict should wrap ErrConflict")
	}
	if !errors.Is(Unsafe("k", errors.New("x")), object.ErrUnsafePath) {
		t.Fatal("Unsafe should wrap ErrUnsafePath")
	}
}
