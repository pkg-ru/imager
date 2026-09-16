// C-обёртка над libheif sequence API (libheif >= 1.23) для govips.
//
// Зачем нужна обёртка: libvips heifsave НЕ пишет animation track для
// multi-page HEIF/AVIF (кадры сохраняются как отдельные hvc1/av01 items без
// eqiv/anip/delay боксов) — анимированный AVIF на выходе получается
// «статичным» (плееры показывают только первый кадр). Нативный libheif
// sequence encoder (heif_sequences.h) умеет писать настоящий animation track
// (brand avis, moov/trak/stbl, per-frame duration, repetitions).
//
// Обёртка предоставляет минимальный набор функций, необходимых для
// кодирования последовательности кадров в память:
//   - создание контекста/энкодера (AV1/aom);
//   - создание visual sequence track (pict) с timescale;
//   - передача кадров RGB(A) с per-frame duration;
//   - завершение последовательности;
//   - запись результата в буфер через heif_writer callback.
//
// ВАЖНО (libheif 1.23): передача NULL в sequence_encoding_options приводит к
// segfault (Track_Visual::encode_image разыменовывает in_options->
// save_alpha_channel). Обёртка ВСЕГДА аллоцирует options и передаёт их.
//
// ВАЖНО (libheif 1.23): HEVC (x265) sequence encoding сломан (assert в
// hevc_enc.cc) — поэтому анимированный HEIF/HEIC НЕ кодируется через этот
// путь; используется только AV1 (AVIF).
#include <stdlib.h>
#include <string.h>
#include <libheif/heif.h>
#include <libheif/heif_sequences.h>

// Структура для сбора выходного буфера в heif_writer callback.
typedef struct {
  unsigned char* data;
  size_t size;
  size_t capacity;
} heif_govips_buffer;

// heif_writer callback: дописывает блок данных в буфер.
static heif_error heif_govips_writer_write(struct heif_context* ctx,
                                           const void* data, size_t size,
                                           void* userdata) {
  (void)ctx;
  heif_govips_buffer* buf = (heif_govips_buffer*)userdata;
  if (buf->size + size > buf->capacity) {
    size_t newcap = buf->capacity ? buf->capacity * 2 : 65536;
    while (newcap < buf->size + size) {
      newcap *= 2;
    }
    unsigned char* nd = (unsigned char*)realloc(buf->data, newcap);
    if (!nd) {
      heif_error e;
      e.code = heif_error_Memory_allocation_error;
      e.subcode = heif_suberror_Unspecified;
      e.message = "heif_govips: out of memory";
      return e;
    }
    buf->data = nd;
    buf->capacity = newcap;
  }
  memcpy(buf->data + buf->size, data, size);
  buf->size += size;
  return heif_error_success;
}

// Создаёт контекст libheif. Возвращает NULL при ошибке.
struct heif_context* heif_govips_context_alloc(void) {
  return heif_context_alloc();
}

// Освобождает контекст.
void heif_govips_context_free(struct heif_context* ctx) {
  if (ctx) {
    heif_context_free(ctx);
  }
}

// Получает энкодер для формата. Возвращает 0 при успехе, иначе код ошибки.
int heif_govips_get_encoder(struct heif_context* ctx,
                            enum heif_compression_format fmt,
                            struct heif_encoder** out_enc) {
  struct heif_error e = heif_context_get_encoder_for_format(ctx, fmt, out_enc);
  return e.code;
}

// Освобождает энкодер.
void heif_govips_encoder_release(struct heif_encoder* enc) {
  if (enc) {
    heif_encoder_release(enc);
  }
}

// Устанавливает lossy quality (0-100). Возвращает 0 при успехе.
int heif_govips_encoder_set_quality(struct heif_encoder* enc, int quality) {
  struct heif_error e = heif_encoder_set_lossy_quality(enc, quality);
  return e.code;
}

// Устанавливает lossless режим. Возвращает 0 при успехе.
int heif_govips_encoder_set_lossless(struct heif_encoder* enc, int enable) {
  struct heif_error e = heif_encoder_set_lossless(enc, enable);
  return e.code;
}

// Устанавливает целочисленный параметр энкодера (например "speed").
// Возвращает 0 при успехе.
int heif_govips_encoder_set_integer(struct heif_encoder* enc,
                                    const char* name, int value) {
  struct heif_error e = heif_encoder_set_parameter_integer(enc, name, value);
  return e.code;
}

// Создаёт visual sequence track (pict) с заданным timescale.
// ВАЖНО: sequence_encoding_options ВСЕГДА аллоцируются (обход segfault
// libheif 1.23 при NULL). Возвращает 0 при успехе.
int heif_govips_add_visual_sequence_track(struct heif_context* ctx,
                                          int width, int height,
                                          uint32_t timescale,
                                          struct heif_track** out_track) {
  struct heif_track_options* topts = heif_track_options_alloc();
  if (!topts) {
    return heif_error_Memory_allocation_error;
  }
  heif_track_options_set_timescale(topts, timescale);

  struct heif_sequence_encoding_options* eopts =
      heif_sequence_encoding_options_alloc();
  if (!eopts) {
    heif_track_options_release(topts);
    return heif_error_Memory_allocation_error;
  }

  struct heif_error e = heif_context_add_visual_sequence_track(
      ctx, (uint16_t)width, (uint16_t)height,
      heif_track_type_image_sequence, topts, eopts, out_track);

  heif_track_options_release(topts);
  heif_sequence_encoding_options_release(eopts);
  return e.code;
}

// Устанавливает глобальный timescale последовательности.
void heif_govips_set_sequence_timescale(struct heif_context* ctx,
                                        uint32_t timescale) {
  heif_context_set_sequence_timescale(ctx, timescale);
}

// Устанавливает число повторов последовательности (0 = бесконечно).
void heif_govips_set_sequence_repetitions(struct heif_context* ctx,
                                          uint32_t repetitions) {
  heif_context_set_number_of_sequence_repetitions(ctx, repetitions);
}

// Создаёт heif_image RGB(A) 8-bit и копирует пиксели из буфера.
// has_alpha: 0 = RGB (3 канала), 1 = RGBA (4 канала).
// Возвращает 0 при успехе.
int heif_govips_image_create_rgba(int width, int height, int has_alpha,
                                  const unsigned char* pixels,
                                  struct heif_image** out_img) {
  enum heif_chroma chroma =
      has_alpha ? heif_chroma_interleaved_RGBA : heif_chroma_interleaved_RGB;
  struct heif_error e =
      heif_image_create(width, height, heif_colorspace_RGB, chroma, out_img);
  if (e.code) {
    return e.code;
  }
  e = heif_image_add_plane(*out_img, heif_channel_interleaved, width, height, 8);
  if (e.code) {
    heif_image_release(*out_img);
    *out_img = NULL;
    return e.code;
  }
  int stride = 0;
  unsigned char* plane = heif_image_get_plane(*out_img, heif_channel_interleaved,
                                              &stride);
  if (!plane) {
    heif_image_release(*out_img);
    *out_img = NULL;
    return heif_error_Usage_error;
  }
  int channels = has_alpha ? 4 : 3;
  for (int y = 0; y < height; y++) {
    memcpy(plane + (size_t)y * stride, pixels + (size_t)y * width * channels,
           (size_t)width * channels);
  }
  return 0;
}

// Устанавливает длительность кадра в тиках timescale трека.
void heif_govips_image_set_duration(struct heif_image* img, uint32_t duration) {
  heif_image_set_duration(img, duration);
}

// Освобождает heif_image.
void heif_govips_image_release(struct heif_image* img) {
  if (img) {
    heif_image_release(img);
  }
}

// Кодирует кадр в sequence track. Возвращает 0 при успехе.
int heif_govips_track_encode_sequence_image(struct heif_track* track,
                                            struct heif_image* img,
                                            struct heif_encoder* enc) {
  struct heif_sequence_encoding_options* eopts =
      heif_sequence_encoding_options_alloc();
  if (!eopts) {
    return heif_error_Memory_allocation_error;
  }
  struct heif_error e =
      heif_track_encode_sequence_image(track, img, enc, eopts);
  heif_sequence_encoding_options_release(eopts);
  return e.code;
}

// Завершает последовательность. Возвращает 0 при успехе.
int heif_govips_track_encode_end_of_sequence(struct heif_track* track,
                                             struct heif_encoder* enc) {
  struct heif_error e = heif_track_encode_end_of_sequence(track, enc);
  return e.code;
}

// Записывает контекст в буфер. Возвращает 0 при успехе.
// out_data/out_size: аллоцированный буфер (free через heif_govips_buffer_free).
int heif_govips_context_write(struct heif_context* ctx,
                              unsigned char** out_data, size_t* out_size) {
  heif_govips_buffer buf;
  buf.data = NULL;
  buf.size = 0;
  buf.capacity = 0;

  struct heif_writer writer;
  writer.writer_api_version = 1;
  writer.write = heif_govips_writer_write;

  struct heif_error e = heif_context_write(ctx, &writer, &buf);
  if (e.code) {
    if (buf.data) {
      free(buf.data);
    }
    return e.code;
  }
  *out_data = buf.data;
  *out_size = buf.size;
  return 0;
}

// Освобождает буфер, возвращённый heif_govips_context_write.
void heif_govips_buffer_free(unsigned char* data) {
  if (data) {
    free(data);
  }
}

// --- Верификация (используется в тестах) ---

// Проверяет, что в файле есть sequence track. Возвращает 1/0.
int heif_govips_has_sequence(const unsigned char* data, size_t size) {
  struct heif_context* ctx = heif_context_alloc();
  if (!ctx) {
    return 0;
  }
  struct heif_error e = heif_context_read_from_memory_without_copy(
      ctx, data, size, NULL);
  if (e.code) {
    heif_context_free(ctx);
    return 0;
  }
  int has = heif_context_has_sequence(ctx);
  heif_context_free(ctx);
  return has;
}

// Возвращает число кадров в sequence track (0 при ошибке/отсутствии).
int heif_govips_sequence_frame_count(const unsigned char* data, size_t size) {
  struct heif_context* ctx = heif_context_alloc();
  if (!ctx) {
    return 0;
  }
  struct heif_error e = heif_context_read_from_memory_without_copy(
      ctx, data, size, NULL);
  if (e.code) {
    heif_context_free(ctx);
    return 0;
  }
  if (!heif_context_has_sequence(ctx)) {
    heif_context_free(ctx);
    return 0;
  }
  struct heif_track* track = heif_context_get_track(ctx, 0);
  if (!track) {
    heif_context_free(ctx);
    return 0;
  }
  int count = 0;
  struct heif_decoding_options* dopts = heif_decoding_options_alloc();
  if (dopts) {
    // ВАЖНО (libheif >= 1.21): по умолчанию heif_track_decode_next_image()
    // применяет edit list трека. Для файлов с бесконечным повтором
    // (repetitions=0, как loop=0 в GIF) итерация до End_of_sequence
    // НИКОГДА не завершается. Флаг ignore_sequence_editlist заставляет
    // libheif проигрывать медиа-таймлайн ровно один раз.
    dopts->ignore_sequence_editlist = 1;
  }
  for (;;) {
    struct heif_image* img = NULL;
    struct heif_error de = heif_track_decode_next_image(
        track, &img, heif_colorspace_undefined, heif_chroma_undefined, dopts);
    if (de.code) {
      // heif_error_End_of_sequence (13) — штатное завершение.
      break;
    }
    if (!img) {
      // Защита от бесконечного цикла при некорректном файле.
      break;
    }
    heif_image_release(img);
    count++;
    if (count > 100000) {
      // Safety cap: не даём зависнуть на битом/нестандартном файле.
      break;
    }
  }
  if (dopts) {
    heif_decoding_options_free(dopts);
  }
  heif_track_release(track);
  heif_context_free(ctx);
  return count;
}

// Возвращает длительность первого кадра в тиках (0 при ошибке).
uint32_t heif_govips_sequence_first_duration(const unsigned char* data,
                                             size_t size) {
  struct heif_context* ctx = heif_context_alloc();
  if (!ctx) {
    return 0;
  }
  struct heif_error e = heif_context_read_from_memory_without_copy(
      ctx, data, size, NULL);
  if (e.code) {
    heif_context_free(ctx);
    return 0;
  }
  if (!heif_context_has_sequence(ctx)) {
    heif_context_free(ctx);
    return 0;
  }
  struct heif_track* track = heif_context_get_track(ctx, 0);
  if (!track) {
    heif_context_free(ctx);
    return 0;
  }
  struct heif_decoding_options* dopts = heif_decoding_options_alloc();
  if (dopts) {
    // См. heif_govips_sequence_frame_count: без ignore_sequence_editlist
    // decode на файле с бесконечным повтором не завершается.
    dopts->ignore_sequence_editlist = 1;
  }
  struct heif_image* img = NULL;
  struct heif_error de = heif_track_decode_next_image(
      track, &img, heif_colorspace_undefined, heif_chroma_undefined, dopts);
  uint32_t dur = 0;
  if (!de.code && img) {
    dur = heif_image_get_duration(img);
    heif_image_release(img);
  }
  if (dopts) {
    heif_decoding_options_free(dopts);
  }
  heif_track_release(track);
  heif_context_free(ctx);
  return dur;
}