// Package pixel предоставляет генератор прозрачных 1x1 пикселей для
// not-found fallback (httpapi.PixelGenerator).
//
// Файлы лежат в adapters/pixel/pixels/not-found-pixel.* и встраиваются в
// бинарь через go:embed — ноль зависимостей от внешних бинарников
// (ImageMagick) и ноль чтения с диска в рантайме.
package pixel

import (
	"context"
	"embed"
	"errors"
	"strings"
)

// Встроенные прозрачные 1x1 пиксели для not-found fallback.
//
//go:embed pixels/not-found-pixel.*
var pixelFS embed.FS

// Generator — PixelGenerator, отдающий встроенные файлы пикселей.
// Реализует httpapi.PixelGenerator без subprocess.
type Generator struct{}

// New создаёт генератор пикселей на основе встроенных файлов.
// Не требует ImageMagick.
func New() *Generator {
	return &Generator{}
}

// GeneratePixel возвращает байты прозрачного 1x1 в запрошенном формате.
// Формат — расширение без точки (например "png", "apng", "webp"). Если
// встроенного файла для формата нет — возвращается ошибка (вызывающий
// перейдёт к следующему not-found fallback).
//
// Сигнатура совпадает с httpapi.PixelGenerator, поэтому *Generator
// удовлетворяет этому интерфейсу структурно.
func (p *Generator) GeneratePixel(_ context.Context, format string) ([]byte, error) {
	// Нормализуем формат: убираем ведущую точку и приводим к нижнему
	// регистру (outputFormat в handler уже возвращает расширение без точки).
	ext := strings.ToLower(strings.TrimPrefix(format, "."))
	if ext == "" {
		return nil, errors.New("pixel: empty format")
	}
	// Имя файла: pixels/not-found-pixel.<ext> (путь относительно каталога
	// файла, заданного в директиве go:embed).
	name := "pixels/not-found-pixel." + ext
	f, err := pixelFS.ReadFile(name)
	if err != nil {
		return nil, errors.New("pixel: no embedded pixel for format " + ext)
	}
	return f, nil
}
