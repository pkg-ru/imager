// Package static предоставляет встроенные статические файлы сервиса
// (favicon). Файлы встраиваются в бинарь через go:embed — ноль чтения с
// диска в рантайме и корректная работа в Docker с read-only root fs.
package static

import "embed"

// Встроенные favicon-файлы: ICO-контейнер (16/32/48/256) и PNG для
// современных браузеров.
//
//go:embed favicon.ico favicon-16x16.png favicon-32x32.png
var FS embed.FS
