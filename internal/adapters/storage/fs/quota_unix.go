//go:build unix

package fs

import "syscall"

// errNoSpace и errQuotaExceeded — цели для errors.Is в quotaErr (store.go).
// На UNIX-подобных платформах это соответствующие errno: так распознаются
// реальные ошибки ОС при переполнении диска или превышении квоты.
var (
	errNoSpace       error = syscall.ENOSPC
	errQuotaExceeded error = syscall.EDQUOT
)
