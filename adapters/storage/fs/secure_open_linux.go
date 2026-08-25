//go:build linux

package fs

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// secureOpenFile открывает файл относительно root атомарно с запретом обхода
// любых symlink во всём пути.
//
// На Linux используется openat2(2) с флагами RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS:
//   - RESOLVE_BENEATH запрещает выход за пределы rootFd (root containment);
//   - RESOLVE_NO_SYMLINKS запрещает все symlink в пути.
//
// Это закрывает TOCTOU-окно для ВСЕХ компонентов пути атомарно: root
// открывается один раз как каталог, затем весь относительный путь
// резолвится ядром в одном системном вызове относительно rootFd.
//
// Если openat2 недоступен (ядро без его поддержки: ENOSYS/EINVAL),
// выполняется fallback на openat(2) с O_NOFOLLOW: он атомарно защищает
// последний компонент и привязывает путь к rootFd (промежуточные каталоги —
// best-effort).
func secureOpenFile(root string, rel string, flag int, perm os.FileMode) (*os.File, error) {
	full := filepath.Join(root, rel)
	rootFd, err := unix.Open(root, unix.O_DIRECTORY|unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: full, Err: err}
	}
	defer unix.Close(rootFd)

	fd, err := openat2NoSymlink(rootFd, rel, flag, perm)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: full, Err: err}
	}
	return os.NewFile(uintptr(fd), full), nil
}

// openat2NoSymlink открывает rel относительно dirfd через openat2 с
// RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS. При недоступности openat2 (ENOSYS/
// EINVAL на старых ядрах) выполняет fallback на openat с O_NOFOLLOW.
func openat2NoSymlink(dirfd int, rel string, flag int, perm os.FileMode) (int, error) {
	how := &unix.OpenHow{
		Flags:   uint64(flag | unix.O_NOFOLLOW),
		Mode:    uint64(perm.Perm()),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV,
	}
	fd, err := unix.Openat2(dirfd, rel, how)
	if err == nil {
		return fd, nil
	}
	// Старое ядро без openat2: fallback на openat + O_NOFOLLOW.
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) {
		return unix.Openat(dirfd, rel, flag|unix.O_NOFOLLOW, uint32(perm.Perm()))
	}
	return -1, err
}
