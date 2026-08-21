//go:build !windows && !unix

package fs

// fsyncDir — fallback для платформ без поддержки fsync каталога.
func fsyncDir(dir string) {
	_ = dir
}
