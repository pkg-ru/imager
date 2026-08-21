package httpapi

import (
	"os"
)

// openFallback открывает статический файл fallback. Используется вместо
// http.ServeFile, чтобы гарантировать явный статус 404 до записи body.
func openFallback(file string) (*os.File, error) {
	return os.Open(file)
}
