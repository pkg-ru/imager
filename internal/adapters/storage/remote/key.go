// Package remote содержит общие вспомогательные примитивы для удалённых
// storage-адаптеров (S3, SFTP, FTP, FTPS): безопасную нормализацию ключей,
// ограниченный временный spool для source-потоков и маппинг ошибок в
// типизированные ошибки domain/object.
//
// Адаптеры не должны класть секреты в URI или логи: конфигурация передаёт
// учётные данные отдельными полями, а не в составе URL.
package remote

import (
	"strings"

	"github.com/pkg-ru/imager/internal/domain/object"
)

// CanonicalKey нормализует ObjectKey в безопасный канонический ключ для
// удалённого хранилища. Гарантирует:
//   - "/" как разделитель, без ведущего "/";
//   - отсутствие "." и ".." сегментов;
//   - отсутствие обратных слешей и NUL-байтов;
//   - непустой результат.
//
// При недопустимом ключе возвращается object.ErrUnsafePath.
func CanonicalKey(key object.ObjectKey) (string, error) {
	raw := string(key)
	if raw == "" {
		return "", object.ErrUnsafePath
	}
	if strings.ContainsRune(raw, '\\') {
		return "", object.ErrUnsafePath
	}
	if strings.ContainsRune(raw, 0) {
		return "", object.ErrUnsafePath
	}
	clean := strings.Trim(raw, "/")
	parts := strings.Split(clean, "/")
	var out []string
	for _, p := range parts {
		switch p {
		case "", ".":
			continue
		case "..":
			return "", object.ErrUnsafePath
		default:
			if strings.ContainsAny(p, "\\") {
				return "", object.ErrUnsafePath
			}
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return "", object.ErrUnsafePath
	}
	return strings.Join(out, "/"), nil
}

// SafeKey сообщает, является ли key допустимым каноническим ключом.
func SafeKey(key object.ObjectKey) bool {
	_, err := CanonicalKey(key)
	return err == nil
}
