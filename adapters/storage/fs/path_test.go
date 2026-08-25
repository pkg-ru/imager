package fs

import (
	"path/filepath"
	"testing"

	"github.com/pkg-ru/imager/domain/object"
)

func TestCleanRelContainment(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")

	valid := []object.ObjectKey{
		"a.jpg",
		"a/b/c.jpg",
		"./a.jpg",
		"a/./b.jpg",
		"/a/b.jpg",
		"a//b.jpg",
	}
	for _, key := range valid {
		t.Run("valid/"+string(key), func(t *testing.T) {
			rel, err := cleanRel(root, key)
			if err != nil {
				t.Fatalf("cleanRel(%q) unexpected error: %v", key, err)
			}
			full := filepath.Join(root, rel)
			if !within(root, full) {
				t.Fatalf("cleanRel(%q) escaped root: %q", key, full)
			}
		})
	}

	invalid := []object.ObjectKey{
		"../escape.jpg",
		"a/../../escape.jpg",
		"..",
		"",
		"a\\b.jpg", // Windows-разделитель в сегменте
	}
	for _, key := range invalid {
		t.Run("invalid/"+string(key), func(t *testing.T) {
			if _, err := cleanRel(root, key); err == nil {
				t.Fatalf("cleanRel(%q) expected error, got none", key)
			}
		})
	}
}

func TestCleanRelEmptyRoot(t *testing.T) {
	if _, err := cleanRel("", "a.jpg"); err == nil {
		t.Fatalf("expected error for empty root")
	}
}

// TestCleanRelRejectsUnsafeKeys — regression для fuzz-найденного дефекта:
// cleanRel("0//0") ошибочно отклонялся. Проверяет, что ключи со схлопываемыми
// пустыми/"."-сегментами принимаются, а небезопасные — отклоняются.
func TestCleanRelRejectsUnsafeKeys(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	cases := []object.ObjectKey{
		"0//0",        // двойной слеш — пустой сегмент схлопывается
		"a/./b.jpg",   // "." сегмент
		"./a.jpg",     // ведущий "."
		"/a/b.jpg",    // ведущий слеш
		"a//b//c.jpg", // множественные пустые сегменты
	}
	for _, key := range cases {
		if _, err := cleanRel(root, key); err != nil {
			t.Fatalf("cleanRel(%q) unexpected error: %v", key, err)
		}
	}

	rejected := []object.ObjectKey{
		"a/../b.jpg",     // ".." — консервативно отклоняется
		"a\\b.jpg",       // обратный слеш
		".meta/x",        // зарезервированный сегмент (каталог sidecar-метаданных)
		"photos/.meta/x", // сегмент ".meta" внутри ключа (посегментная проверка)
		".meta",          // сам сегмент ".meta"
		".tmp-x/y",       // зарезервированный префикс
		".tmp-meta-x",    // temp-файлы записи метаданных
		"a/.tmp-meta-y",  // префикс внутри пути
		".",              // только "." — нет значимого сегмента
		"//",             // только пустые сегменты
		"\x00",           // NUL-байт недопустим в пути
		"a\x00b.jpg",     // NUL внутри сегмента
	}
	for _, key := range rejected {
		if _, err := cleanRel(root, key); err == nil {
			t.Fatalf("cleanRel(%q) expected error, got none", key)
		}
	}
}
