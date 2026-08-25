//go:build windows

package fs

import "syscall"

// errNoSpace и errQuotaExceeded — цели для errors.Is в quotaErr (store.go).
// На Windows используются syscall.ENOSPC/syscall.EDQUOT, что сохраняет
// поведение, идентичное UNIX-платформам.
var (
	errNoSpace       error = syscall.ENOSPC
	errQuotaExceeded error = syscall.EDQUOT
)
