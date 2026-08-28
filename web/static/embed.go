// Package static предоставляет встроенные статические файлы сервиса
// (favicon). Файлы встраиваются в бинарь через go:embed — ноль чтения с
package static

import "embed"

//go:embed favicon.ico
var FS embed.FS
