// Go-биндинг над libheif sequence API (libheif >= 1.23) для govips.
//
// Назначение: кодирование анимированного AVIF через нативный libheif
// sequence encoder. libvips heifsave НЕ пишет animation track для multi-page
// HEIF/AVIF (кадры сохраняются как отдельные items без eqiv/anip/delay
// боксов) — анимированный AVIF на выходе получается «статичным». Нативный
// libheif sequence encoder пишет настоящий animation track (brand avis,
// moov/trak/stbl, per-frame duration, repetitions).
//
// C-обёртка (heif_sequence.c) инкапсулирует работу с libheif; этот файл
// предоставляет безопасный Go API поверх неё.
//
// ВАЖНО: файл использует собственную cgo-директиву `pkg-config: vips libheif`
// (не наследует директиву из govips.go). Это позволяет подключать libheif
// только там, где он нужен, не меняя остальной govips.
package vips

/*
#cgo pkg-config: vips libheif
#include <stdlib.h>
#include <vips/vips.h>
#include <libheif/heif.h>
#include <libheif/heif_sequences.h>

// Обёртки из heif_sequence.c
struct heif_context* heif_govips_context_alloc(void);
void heif_govips_context_free(struct heif_context* ctx);
int heif_govips_get_encoder(struct heif_context* ctx, enum heif_compression_format fmt, struct heif_encoder** out_enc);
void heif_govips_encoder_release(struct heif_encoder* enc);
int heif_govips_encoder_set_quality(struct heif_encoder* enc, int quality);
int heif_govips_encoder_set_lossless(struct heif_encoder* enc, int enable);
int heif_govips_encoder_set_integer(struct heif_encoder* enc, const char* name, int value);
int heif_govips_add_visual_sequence_track(struct heif_context* ctx, int width, int height, uint32_t timescale, struct heif_track** out_track);
void heif_govips_set_sequence_timescale(struct heif_context* ctx, uint32_t timescale);
void heif_govips_set_sequence_repetitions(struct heif_context* ctx, uint32_t repetitions);
int heif_govips_image_create_rgba(int width, int height, int has_alpha, const unsigned char* pixels, struct heif_image** out_img);
void heif_govips_image_set_duration(struct heif_image* img, uint32_t duration);
void heif_govips_image_release(struct heif_image* img);
int heif_govips_track_encode_sequence_image(struct heif_track* track, struct heif_image* img, struct heif_encoder* enc);
int heif_govips_track_encode_end_of_sequence(struct heif_track* track, struct heif_encoder* enc);
int heif_govips_context_write(struct heif_context* ctx, unsigned char** out_data, size_t* out_size);
void heif_govips_buffer_free(unsigned char* data);

// Верификация (используется в тестах)
int heif_govips_has_sequence(const unsigned char* data, size_t size);
int heif_govips_sequence_frame_count(const unsigned char* data, size_t size);
uint32_t heif_govips_sequence_first_duration(const unsigned char* data, size_t size);
*/
import "C"

import (
	"errors"
	"fmt"
	"runtime"
	"unsafe"
)

// HeifCompressionFormat — формат сжатия libheif (AV1/HEVC).
type HeifCompressionFormat C.enum_heif_compression_format

const (
	HeifCompressionAV1  HeifCompressionFormat = C.heif_compression_AV1
	HeifCompressionHEVC HeifCompressionFormat = C.heif_compression_HEVC
)

// HeifSequenceEncoder — обёртка над libheif sequence encoder.
//
// Жизненный цикл: NewHeifSequenceEncoder → AddFrame (N раз) → Finish →
// Bytes → Close. Контекст и энкодер создаются ОДИН раз на ассет, кадры
// передаются последовательно (без повторного декодирования/ресайза).
type HeifSequenceEncoder struct {
	ctx   *C.struct_heif_context
	enc   *C.struct_heif_encoder
	track *C.struct_heif_track
	// w/h — размер кадра (все кадры одного размера).
	w, h int
	// finished — true после Finish (последующие AddFrame запрещены).
	finished bool
}

// HeifSequenceParams — параметры кодирования анимированного AVIF.
type HeifSequenceParams struct {
	// Width/Height — размер кадра (все кадры одного размера).
	Width  int
	Height int
	// Timescale — тиков в секунду (1000 = длительности в миллисекундах).
	Timescale uint32
	// Repetitions — число повторов (0 = бесконечно).
	Repetitions uint32
	// Quality — lossy quality 0-100 (используется при Lossless=false).
	Quality int
	// Lossless — lossless-режим.
	Lossless bool
	// Speed — параметр "speed" энкодера aom (0-10, 0 = медленно/лучше).
	Speed int
}

// NewHeifSequenceEncoder создаёт sequence encoder для формата fmt (AV1).
//
// Контекст и энкодер создаются здесь — ОДИН раз на ассет. При ошибке
// возвращается nil, err (все внутренние ресурсы освобождены).
func NewHeifSequenceEncoder(compression HeifCompressionFormat, params HeifSequenceParams) (*HeifSequenceEncoder, error) {
	ctx := C.heif_govips_context_alloc()
	if ctx == nil {
		return nil, fmt.Errorf("libheif: context alloc failed")
	}

	var enc *C.struct_heif_encoder
	if code := C.heif_govips_get_encoder(ctx, C.enum_heif_compression_format(compression), &enc); code != 0 {
		C.heif_govips_context_free(ctx)
		return nil, fmt.Errorf("libheif: get encoder for format %d: code %d", compression, code)
	}

	// Quality/lossless/speed применяются к энкодеру.
	if params.Lossless {
		if code := C.heif_govips_encoder_set_lossless(enc, 1); code != 0 {
			C.heif_govips_encoder_release(enc)
			C.heif_govips_context_free(ctx)
			return nil, fmt.Errorf("libheif: set lossless: code %d", code)
		}
	} else {
		if code := C.heif_govips_encoder_set_quality(enc, C.int(params.Quality)); code != 0 {
			C.heif_govips_encoder_release(enc)
			C.heif_govips_context_free(ctx)
			return nil, fmt.Errorf("libheif: set quality %d: code %d", params.Quality, code)
		}
	}
	if params.Speed > 0 {
		cName := C.CString("speed")
		code := C.heif_govips_encoder_set_integer(enc, cName, C.int(params.Speed))
		C.free(unsafe.Pointer(cName))
		if code != 0 {
			C.heif_govips_encoder_release(enc)
			C.heif_govips_context_free(ctx)
			return nil, fmt.Errorf("libheif: set speed %d: code %d", params.Speed, code)
		}
	}

	// Глобальный timescale и repetitions.
	if params.Timescale > 0 {
		C.heif_govips_set_sequence_timescale(ctx, C.uint32_t(params.Timescale))
	}
	C.heif_govips_set_sequence_repetitions(ctx, C.uint32_t(params.Repetitions))

	// Visual sequence track (pict). Обёртка всегда аллоцирует
	// sequence_encoding_options (обход segfault libheif 1.23 при NULL).
	var track *C.struct_heif_track
	if code := C.heif_govips_add_visual_sequence_track(ctx, C.int(params.Width), C.int(params.Height), C.uint32_t(params.Timescale), &track); code != 0 {
		C.heif_govips_encoder_release(enc)
		C.heif_govips_context_free(ctx)
		return nil, fmt.Errorf("libheif: add visual sequence track: code %d", code)
	}

	e := &HeifSequenceEncoder{
		ctx:   ctx,
		enc:   enc,
		track: track,
		w:     params.Width,
		h:     params.Height,
	}
	runtime.SetFinalizer(e, (*HeifSequenceEncoder).Close)
	return e, nil
}

// AddFrame добавляет кадр в последовательность.
//
// pixels — пиксели RGB (3 канала) или RGBA (4 канала) 8-bit, top-down,
// размер Width*Height*channels. duration — длительность кадра в тиках
// timescale (например 100 при timescale=1000 = 100 мс).
func (e *HeifSequenceEncoder) AddFrame(pixels []byte, hasAlpha bool, duration uint32) error {
	if e == nil || e.ctx == nil {
		return fmt.Errorf("libheif: encoder closed")
	}
	if e.finished {
		return fmt.Errorf("libheif: add frame after finish")
	}
	channels := 3
	if hasAlpha {
		channels = 4
	}
	// Проверка размера буфера (защита от переполнения).
	if len(pixels) < e.frameSize(channels) {
		return fmt.Errorf("libheif: frame buffer too small: got %d, want %d", len(pixels), e.frameSize(channels))
	}

	var img *C.struct_heif_image
	code := C.heif_govips_image_create_rgba(C.int(e.width()), C.int(e.height()), boolToCInt(hasAlpha), (*C.uchar)(unsafe.Pointer(&pixels[0])), &img)
	if code != 0 {
		return fmt.Errorf("libheif: create frame image: code %d", code)
	}
	defer C.heif_govips_image_release(img)

	C.heif_govips_image_set_duration(img, C.uint32_t(duration))

	if code := C.heif_govips_track_encode_sequence_image(e.track, img, e.enc); code != 0 {
		return fmt.Errorf("libheif: encode sequence image: code %d", code)
	}
	return nil
}

// Finish завершает последовательность (end of sequence).
func (e *HeifSequenceEncoder) Finish() error {
	if e == nil || e.ctx == nil {
		return fmt.Errorf("libheif: encoder closed")
	}
	if e.finished {
		return nil
	}
	if code := C.heif_govips_track_encode_end_of_sequence(e.track, e.enc); code != 0 {
		return fmt.Errorf("libheif: end of sequence: code %d", code)
	}
	e.finished = true
	return nil
}

// Bytes записывает результат в буфер. Может вызываться после Finish.
// Возвращённый буфер принадлежит вызывающему.
func (e *HeifSequenceEncoder) Bytes() ([]byte, error) {
	if e == nil || e.ctx == nil {
		return nil, fmt.Errorf("libheif: encoder closed")
	}
	var out *C.uchar
	var outSize C.size_t
	if code := C.heif_govips_context_write(e.ctx, &out, &outSize); code != 0 {
		return nil, fmt.Errorf("libheif: write context: code %d", code)
	}
	defer C.heif_govips_buffer_free(out)
	if outSize == 0 {
		return nil, fmt.Errorf("libheif: empty output")
	}
	return C.GoBytes(unsafe.Pointer(out), C.int(outSize)), nil
}

// Close освобождает все ресурсы (контекст, энкодер, трек).
func (e *HeifSequenceEncoder) Close() {
	if e == nil {
		return
	}
	if e.track != nil {
		C.heif_track_release(e.track)
		e.track = nil
	}
	if e.enc != nil {
		C.heif_govips_encoder_release(e.enc)
		e.enc = nil
	}
	if e.ctx != nil {
		C.heif_govips_context_free(e.ctx)
		e.ctx = nil
	}
}

// width возвращает ширину кадра.
func (e *HeifSequenceEncoder) width() int {
	return e.w
}

// height возвращает высоту кадра.
func (e *HeifSequenceEncoder) height() int {
	return e.h
}

// frameSize возвращает размер буфера кадра в байтах.
func (e *HeifSequenceEncoder) frameSize(channels int) int {
	return e.w * e.h * channels
}

// boolToCInt конвертирует bool в C int (0/1).
func boolToCInt(b bool) C.int {
	if b {
		return 1
	}
	return 0
}

// RawRGBAPixels возвращает сырые пиксели изображения в sRGB 8-bit.
//
// Используется для передачи кадров в нативный libheif sequence encoder
// (анимированный AVIF): результат — top-down пиксели RGB (3 канала) или
// RGBA (4 канала), hasAlpha=true при наличии альфа-канала. Изображение
// конвертируется в sRGB и кастится в uchar (как ToGoImage), но без
// материализации в image.Image — только сырой буфер.
func (r *ImageRef) RawRGBAPixels() (pixels []byte, hasAlpha bool, err error) {
	defer runtime.KeepAlive(r)

	tmp, err := vipsGenCopy(r.image, nil)
	if err != nil {
		return nil, false, err
	}
	defer clearImage(tmp)

	// Convert to sRGB if needed (keep B_W for grayscale)
	interp := Interpretation(int(tmp.Type))
	if interp != InterpretationSRGB && interp != InterpretationBW {
		out, err := vipsToColorSpace(tmp, InterpretationSRGB)
		if err != nil {
			return nil, false, err
		}
		clearImage(tmp)
		tmp = out
	}

	// Cast to uchar if needed
	if BandFormat(int(tmp.BandFmt)) != BandFormatUchar {
		out, err := vipsGenCast(tmp, BandFormatUchar, nil)
		if err != nil {
			return nil, false, err
		}
		clearImage(tmp)
		tmp = out
	}

	var cSize C.size_t
	cData := C.vips_image_write_to_memory(tmp, &cSize)
	if cData == nil {
		return nil, false, errors.New("failed to write image to memory")
	}
	defer C.free(cData)

	width := int(tmp.Xsize)
	height := int(tmp.Ysize)
	bands := int(tmp.Bands)
	raw := C.GoBytes(unsafe.Pointer(cData), C.int(cSize))

	switch bands {
	case 1:
		// Grayscale → RGB (3 канала).
		out := make([]byte, width*height*3)
		for i := 0; i < width*height; i++ {
			out[i*3+0] = raw[i]
			out[i*3+1] = raw[i]
			out[i*3+2] = raw[i]
		}
		return out, false, nil
	case 2:
		// Grayscale + alpha → RGBA (4 канала).
		out := make([]byte, width*height*4)
		for i := 0; i < width*height; i++ {
			out[i*4+0] = raw[i*2+0]
			out[i*4+1] = raw[i*2+0]
			out[i*4+2] = raw[i*2+0]
			out[i*4+3] = raw[i*2+1]
		}
		return out, true, nil
	case 3:
		return raw, false, nil
	case 4:
		return raw, true, nil
	default:
		return nil, false, fmt.Errorf("unsupported number of bands: %d", bands)
	}
}

// --- Верификация (используется в тестах) ---

// HeifHasSequence проверяет, что буфер содержит sequence track (анимацию).
func HeifHasSequence(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	return C.heif_govips_has_sequence((*C.uchar)(unsafe.Pointer(&data[0])), C.size_t(len(data))) != 0
}

// HeifSequenceFrameCount возвращает число кадров в sequence track.
func HeifSequenceFrameCount(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	return int(C.heif_govips_sequence_frame_count((*C.uchar)(unsafe.Pointer(&data[0])), C.size_t(len(data))))
}

// HeifSequenceFirstDuration возвращает длительность первого кадра в тиках.
func HeifSequenceFirstDuration(data []byte) uint32 {
	if len(data) == 0 {
		return 0
	}
	return uint32(C.heif_govips_sequence_first_duration((*C.uchar)(unsafe.Pointer(&data[0])), C.size_t(len(data))))
}
