# Обработка изображений

## Конвейер генерации

Запрос проходит конвейер (`app/generatev2`):

1. Разбор URL и валидация (`domain/asset`).
2. Разрешение пресета (если preset URL) в канонический запрос.
3. Проверка политики (deny-by-default) и лимитов.
4. Поиск готового результата в result-хранилище по каноническому ключу.
5. Keyed singleflight: параллельные запросы того же ассета дедуплицируются — обработка выполняется один раз, остальные ждут результат.
6. Открытие источника из source-хранилища.
7. Проверка лимитов политики до обработки (размер источника, размеры/DPR запроса).
8. Загрузка кэша детекции для `fc`/`oc`/`fct`/`oct` (подробнее — в разделе «Детекция лиц и объектов»).
9. Обработка движком (libvips) в spillable-буфер: память с переполнением на диск при исчерпании бюджета `application.buffer-max-bytes`.
10. Post-check лимитов (размер выхода) и атомарная публикация в result-хранилище с retry (экспоненциальный backoff, до 3 попыток для transient-ошибок).

Канонический ключ кэша — сам canonical URL без хеширования: закэшированный ассет доступен по человекочитаемому имени.

## Движки

| Движок | Роль | Особенности |
|--------|------|-------------|
| libvips (govips) | Единственный; сборка `-tags libvips` | In-process, без subprocess; все форматы включая APNG (≥ 8.13); smart-crop |

Маршрутизация — `adapters/processor/routing`.

## Операции

Порядок применения: **auto-orient → rotate → flip → trim → crop/resize**.

### Resize

Применяется, когда transform отсутствует. Размер из URL умножается на DPR:

```text
/test-jpg/640x.webp     ширина 640, высота пропорционально
/test-jpg/x400.webp     высота 400
/test-jpg/120x80.webp   точный размер 120x80
/test-jpg/x.webp        исходный размер (конвертация формата)
```

### Crop (`c`)

Центрированная обрезка под целевой размер.

### Smart-crop (`sc`)

Обрезка по attention-области (libvips). Требует сборки с `-tags libvips`.

### Trim (`t`, а также суффикс `t` в `ct`/`sct`/`fct`/`oct`)

Обрезка однотонных полей перед кропом/ресайзом. Настройки глобальные (`processing.default-trim-*`):

- `default-trim-mode`: `auto` — цвет фона определяется по краевому пикселю; `color` — фиксированный `default-trim-color`;
- `default-trim-tolerance`: допуск сравнения `[0,1]`.

### Face-crop (`fc`) и Object-crop (`oc`)

Обрезка по bounding box детекции. Требуют сборки с `-tags libvips,onnx`, C-библиотеки ONNX Runtime и настроенных моделей. Пустой путь модели отключает операцию — запрос вернёт ошибку `501 unsupported_format`.

## Ориентация

| Параметр | Значения | Действие |
|----------|----------|----------|
| `auto-orient` | bool (дефолт `true`) | Автоповорот по EXIF Orientation — фото с камер отображаются правильно |
| `rotate` | `""`/`none`/`90`/`180`/`270` | Фиксированный поворот по часовой стрелке |
| `flip` | `""`/`none`/`horizontal`/`vertical` | Отражение слева-направо / сверху-вниз |

Значения задаются глобально (`processing.default-*`) и переопределяются в пресетах (`auto-orient`, `rotate`, `flip`; `"none"` явно отключает глобальный дефолт). EXIF-метаданные удаляются из результата.

## Водяные знаки

Водяной знак описывается в секции `watermarks` (map: ключ = имя ватермарки) и применяется по имени. Приоритет: пресет → path-policy → `processing.default-watermark`.

```yaml
watermarks:
  logo:
    path: "/etc/imager/watermarks/logo.png"
    position: center        # top | bottom | left | right | center
    repeat: no-repeat       # no-repeat | repeat | repeat-x | repeat-y | round | space
    size: contain           # contain | cover | "200px 50px"
```

Семантика полей соответствует CSS `background-position` / `background-repeat` / `background-size`.

Ограничения:

- libvips реализует position/repeat/size полностью;
- анимированные выходы (GIF/WebP/APNG) с водяным знаком на libvips возвращают ошибку обработки (композит применился бы только к первому кадру).

Файл водяного знака обязан существовать на старте, иначе конфигурация отклоняется.

## Детекция лиц и объектов

Детекция выполняется ONNX-моделями внутри процесса (требуется сборка с `-tags libvips,onnx`):

| Модель | Операция | Назначение |
|--------|----------|------------|
| YuNet (`face-model`) | `fc`/`fct` | Детекция лиц |
| SSD/YOLO-подобная (`object-model`) | `oc`/`oct` | Детекция объектов |

```yaml
detection:
  face-model: "/etc/imager/models/face_detection_yunet_2023mar.onnx"
  object-model: "/etc/imager/models/ssd_mobilenet.onnx"
  confidence-threshold: 0.5   # порог уверенности [0,1], боксы ниже отбрасываются до NMS
  max-objects: 5              # максимум объектов после NMS
  margin: 0.1                 # отступ вокруг бокса как доля его размера (10%)
```

Свойства:

- модели загружаются лениво при первом запросе и кэшируются в памяти; отсутствие файла модели — ошибка при первом обращении, а не при старте;
- результаты детекции кэшируются в sidecar-хранилище метаданных (`metadata.*`): модель вызывается ровно один раз на родительский файл, последующие запросы читают боксы из кэша;
- при trim-вариантах (`fct`/`oct`) детекция применяется к уже подрезанному изображению.

## Анимации

Для GIF/WebP/APNG/HEIF поддерживаются:

- ограничение числа кадров (`frames` в пресете, `policy.global.limits.frames`);
- ограничение длительности (`duration`, мс);
- зацикливание (`loop`: nil = `processing.default-loop`, true = бесконечно, false = однопроходно).

APNG кодируется как multi-page PNG (libvips ≥ 8.13).

## Качество и сжатие

- `quality` пресета (0–100; 0 = `processing.default-quality`) применяется к lossy-форматам (JPEG/WebP);
- PNG управляется уровнем сжатия: `libvips.encoders.png-compression-level` (0–9);
- WebP: `libvips.encoders.webp-reduction-effort` (0–6).

## Лимиты обработки

Три слоя защиты (значения — [CONFIGURATION.md](CONFIGURATION.md)):

1. **Политика** (`policy.global.limits`): размер источника/выхода, размеры, DPR, кадры, длительность — проверяются до и после обработки.
2. **Движок**: libvips `limits.*` (timeout, output-bytes, concurrency, threads, cache).
3. **Application**: `application.output-limit` (bounded writer прерывает запись), context deadline (`http.generate-timeout` → `504`), admission control (`http.max-concurrent-requests` → `503`).

Перегрузка процессора (переполнение очереди слотов) возвращает клиенту `503 overloaded` с `Retry-After: 1`.
