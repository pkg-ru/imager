package fs

import (
	"path/filepath"
	"testing"

	"github.com/pkg-ru/imager/internal/domain/object"
)

// FuzzCleanRelContainment проверяет, что cleanRel никогда не паникует и что
// при успешном результате путь всегда остаётся внутри root (root containment).
// Это чистая функция без внешних сервисов — безопасно по времени и памяти.
func FuzzCleanRelContainment(f *testing.F) {
	seeds := []string{
		"a.jpg",
		"a/b/c.jpg",
		"../escape.jpg",
		"a/../../escape.jpg",
		"a\\b.jpg",
		"/abs/path.jpg",
		".meta/x",
		".tmp-x/y",
		"",
		"..",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		root := filepath.Join(t.TempDir(), "root")
		key := object.ObjectKey(raw)
		rel, err := cleanRel(root, key)
		if err != nil {
			// Ошибка допустима (unsafe/пустой ключ), но не паника.
			return
		}
		full := filepath.Join(root, rel)
		if !within(root, full) {
			t.Fatalf("cleanRel(%q) escaped root: rel=%q full=%q", raw, rel, full)
		}
	})
}
