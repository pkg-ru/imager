package remote

import (
	"errors"
	"testing"

	"gitverse.ru/pkg-ru/imager/domain/object"
)

func TestCanonicalKey(t *testing.T) {
	tests := []struct {
		name string
		key  object.ObjectKey
		want string
		err  bool
	}{
		{"simple", "a.jpg", "a.jpg", false},
		{"nested", "dir/sub/file.bin", "dir/sub/file.bin", false},
		{"leading slash", "/a/b", "a/b", false},
		{"trailing slash", "a/b/", "a/b", false},
		{"double slash", "a//b", "a/b", false},
		{"dot segments", "a/./b", "a/b", false},
		{"empty", "", "", true},
		{"dot only", ".", "", true},
		{"parent escape", "../a", "", true},
		{"parent mid", "a/../b", "", true},
		{"backslash", `a\b`, "", true},
		{"nul byte", "a\x00b", "", true},
		{"only slashes", "///", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CanonicalKey(tt.key)
			if tt.err {
				if !errors.Is(err, object.ErrUnsafePath) {
					t.Fatalf("expected ErrUnsafePath, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}
