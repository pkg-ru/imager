//go:build !windows && !unix

package fs

import "errors"

// errNoSpace и errQuotaExceeded — цели для errors.Is в quotaErr (store.go).
// На платформах без errno ENOSPC/EDQUOT (например plan9) используются
// платформенно-нейтральные ошибки-заглушки: они заведомо не совпадают
// с реальными ошибками ОС, поэтому распознавание квоты отключено.
var (
	errNoSpace       error = errors.New("no space left on device")
	errQuotaExceeded error = errors.New("disk quota exceeded")
)
