//go:build libvips

// Анимированный AVIF через нативный libheif sequence encoder.
//
// Архитектура (см. README):
//   - static → libvips heifsave (без изменений, hot path не затронут);
//   - animated AVIF → libheif sequence encoder (настоящий animation track);
//   - animated HEIF/HEIC → первый кадр → libvips heifsave (статичный выход,
//     документированное поведение — HEIF/HEIC не поддерживают анимированный
//     выход в текущей Imager).
//
// Анимация определяется как Pages() > 1 && len(delay) > 0 (наличие frame
// timing — не просто multipage). Обычный multipage (delay == 0) уходит в
// static path.
package libvips

import (
	"fmt"

	"github.com/davidbyttow/govips/v2/vips"
	"gitverse.ru/pkg-ru/imager/domain/encoding"
)

// isAnimatedImage сообщает, является ли изображение анимацией (наличие
// frame timing): Pages() > 1 && len(delay) > 0. Обычный multipage (кадры
// без задержек) НЕ считается анимацией — уходит в static path.
//
// Дешёвая проверка: только чтение метаданных libvips, без пиксельных
// операций. Вызывается в exportImage перед веткой AVIF; для не-AVIF
// форматов результат не используется (static path не затронут).
func isAnimatedImage(img *vips.ImageRef) bool {
	if img.Pages() <= 1 {
		return false
	}
	delay, err := img.PageDelay()
	if err != nil || len(delay) == 0 {
		return false
	}
	return true
}

// exportAnimatedAvif кодирует анимированное изображение в AVIF через
// нативный libheif sequence encoder.
//
// Кадры извлекаются из вертикального стека (page-height), каждый кадр
// конвертируется в sRGB 8-bit RGB(A) и передаётся в sequence encoder с
// per-frame duration из delay (мс → тики timescale 1000). Loop из исходника
// маппится в repetitions (0 = бесконечно).
//
// Ошибки возвращаются через существующую систему ошибок (никаких
// abort/segfault/leaks: все cgo-ресурсы освобождаются через Close/defer).
func (b *libvipsBackend) exportAnimatedAvif(img *vips.ImageRef, resolved encoding.ResolvedParams) ([]byte, error) {
	n := img.Pages()
	ph := img.PageHeight()
	W := img.Width()
	H := img.Height()
	if n <= 1 || ph <= 0 || H < ph {
		return nil, fmt.Errorf("libvips: animated avif: invalid geometry pages=%d ph=%d H=%d", n, ph, H)
	}

	delay, err := img.PageDelay()
	if err != nil {
		return nil, fmt.Errorf("libvips: animated avif: page delay: %w", err)
	}
	loop := img.Loop()

	// Параметры sequence encoder: timescale 1000 (тики = мс), duration
	// кадра = delay[i] (мс). Repetitions: loop=0 → бесконечно (0),
	// loop=N → N повторов.
	params := vips.HeifSequenceParams{
		Width:       W,
		Height:      ph,
		Timescale:   1000,
		Repetitions: uint32(loop),
		Quality:     resolved.Quality,
		Lossless:    resolved.Lossless,
		Speed:       resolved.Speed,
	}
	enc, err := vips.NewHeifSequenceEncoder(vips.HeifCompressionAV1, params)
	if err != nil {
		return nil, fmt.Errorf("libvips: animated avif: sequence encoder: %w", err)
	}
	defer enc.Close()

	// Кадры извлекаются последовательно из стека; каждый кадр —
	// одиночное изображение высотой ph (как в withFrames).
	for i := 0; i < n; i++ {
		f, err := img.Copy()
		if err != nil {
			return nil, fmt.Errorf("libvips: animated avif: copy frame %d/%d: %w", i+1, n, err)
		}
		if err := f.SetPageHeight(H); err != nil {
			f.Close()
			return nil, fmt.Errorf("libvips: animated avif: set page height frame %d/%d: %w", i+1, n, err)
		}
		if err := f.ExtractArea(0, i*ph, W, ph); err != nil {
			f.Close()
			return nil, fmt.Errorf("libvips: animated avif: extract frame %d/%d: %w", i+1, n, err)
		}

		pixels, hasAlpha, err := f.RawRGBAPixels()
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("libvips: animated avif: frame %d/%d pixels: %w", i+1, n, err)
		}

		// Duration кадра: delay[i] мс; при отсутствии/нуле — 100 мс
		// (дефолт GIF-подобной анимации, libvips использует 100 мс для
		// кадров без delay).
		d := 100
		if i < len(delay) && delay[i] > 0 {
			d = delay[i]
		}
		if err := enc.AddFrame(pixels, hasAlpha, uint32(d)); err != nil {
			return nil, fmt.Errorf("libvips: animated avif: encode frame %d/%d: %w", i+1, n, err)
		}
	}

	if err := enc.Finish(); err != nil {
		return nil, fmt.Errorf("libvips: animated avif: end of sequence: %w", err)
	}
	out, err := enc.Bytes()
	if err != nil {
		return nil, fmt.Errorf("libvips: animated avif: write: %w", err)
	}
	return out, nil
}
