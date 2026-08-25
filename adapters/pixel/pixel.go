package main

import (
	"context"
	"embed"
	"errors"
	"strings"

	"github.com/pkg-ru/imager/internal/adapters/httpapi"
)

// Встроенные прозрачные 1x1 пиксели для not-found fallback. Файлы лежат в
// cmd/imager/pixels/not-found-pixel.* и встраиваются в бинарь через go:embed —
// ноль зависимостей от внешних бинарников (ImageMagick) и ноль чтения с диска
// в рантайме.
//
//go:embed pixels/not-found-pixel.*
var pixelFS embed.FS

// embedPixelGenerator — PixelGenerator, отдающий встроенные файлы пикселей.
// Реализует httpapi.PixelGenerator без subprocess.
type embedPixelGenerator struct{}

var _ httpapi.PixelGenerator = (*embedPixelGenerator)(nil)

// newEmbedPixelGenerator создаёт генератор пикселей на основе встроенных
// файлов. Не требует ImageMagick.
func newEmbedPixelGenerator() *embedPixelGenerator {
	return &embedPixelGenerator{}
}

// GeneratePixel возвращает байты прозрачного 1x1 в запрошенном формате.
// Формат — расширение без точки (например "png", "apng", "webp"). Если
// встроенного файла для формата нет — возвращается ошибка (вызывающий
// перейдёт к следующему not-found fallback).
func (p *embedPixelGenerator) GeneratePixel(_ context.Context, format string) ([]byte, error) {
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
