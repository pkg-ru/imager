//go:build !windows && !unix

package fs

// fsyncDir — fallback для платформ без поддержки fsync каталога.
// Возвращает nil: на таких платформах durability не гарантируется.
func fsyncDir(dir string) error {
	_ = dir
	return nil
}
