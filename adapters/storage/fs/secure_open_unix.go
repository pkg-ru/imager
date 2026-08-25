//go:build unix && !linux

package fs

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// secureOpenFile открывает файл относительно root с запретом обхода symlink
// в последнем компоненте (O_NOFOLLOW) и с привязкой к root через dirfd
// (openat). Это закрывает TOCTOU-окно для последнего компонента атомарно:
// root открывается один раз как каталог, затем файл открывается по имени
// внутри него. Промежуточные каталоги root открываются через unix.Open с
// следованием symlink (best-effort).
//
// На Linux используется более строгая реализация openat2 с
// RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS (см. secure_open_linux.go), которая
// атомарно защищает все компоненты пути.
func secureOpenFile(root string, rel string, flag int, perm os.FileMode) (*os.File, error) {
	full := filepath.Join(root, rel)
	rootFd, err := unix.Open(root, unix.O_DIRECTORY|unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: full, Err: err}
	}
	defer unix.Close(rootFd)
	fd, err := unix.Openat(rootFd, rel, flag|unix.O_NOFOLLOW, uint32(perm.Perm()))
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: full, Err: err}
	}
	return os.NewFile(uintptr(fd), full), nil
}
