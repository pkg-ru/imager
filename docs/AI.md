# AI-возможности

Imager поддерживает AI-кропы: умный кроп (smart-crop), кроп по лицам (face-crop) и кроп по объектам (object-crop). Детекция выполняется через ONNX Runtime с двумя моделями: YuNet (лица) и SSD MobileNet v1 (объекты).

Связанные документы: [ARCHITECTURE.md](ARCHITECTURE.md), [PROCESSING.md](PROCESSING.md), [CONFIGURATION.md](CONFIGURATION.md), [FORMATS.md](FORMATS.md).

## Операции

| Операция | Описание |
|----------|----------|
| `smart-crop` | Умный кроп: выбор наиболее «информативной» области изображения. |
| `face-crop` | Кроп по лицам: центрирование на обнаруженных лицах. |
| `object-crop` | Кроп по объектам: центрирование на обнаруженных объектах (COCO). |
| `face-fix-crop` | Кроп по лицам с фиксацией (fallback на smart-crop при отсутствии лиц). |
| `object-fix-crop` | Кроп по объектам с фиксацией (fallback на smart-crop при отсутствии объектов). |

## Модели

### YuNet (детекция лиц)

- Файл: `face_detection_yunet_2023mar.onnx`.
- Вход: letterbox 640×640, raw-пиксели (0–255), **без нормализации**.
- Padding: 127.5.
- Score: `cls * obj` (clamped).
- Выход: боксы лиц.

### SSD MobileNet v1 (детекция объектов)

- Файл: `ssd_mobilenet_v1_12.onnx`.
- Вход: 300×300, stretch-resize (без сохранения пропорций), uint8 NHWC.
- Классы: COCO 1–90, метки вида `COCO_<name>`.
- Выход: боксы объектов с классами.

## ONNX Runtime

- Сборка с тегом `onnx` (CGO + ONNX Runtime).
- Модели загружаются **лениво** (lazy loading) при первом использовании.
- Пути к моделям задаются в конфигурации (`detection.face-model` / `detection.object-model`); пустой путь = операция отключена.

## Конвейер детекции

1. Запрос берёт слот libvips.
2. При необходимости детекции — handoff: слот libvips освобождается, берётся слот детекции (семафор детекции).
3. Детекция выполняется (ONNX).
4. Слот libvips возвращается (reacquire).
5. Кроп применяется к изображению.

### Семафор детекции

- `detection.concurrency`: по умолчанию `max(1, GOMAXPROCS/2)`.
- Максимальное ожидание слота: 5 секунд (`detection.max-wait`).
- При недоступности слота или ошибке модели — **graceful degradation**: умный кроп заменяется center-crop.

## Параметры детекции

| Параметр | По умолчанию (код) | Shipped-конфиг | Описание |
|----------|:------------------:|:--------------:|----------|
| `confidence-threshold` | 0.5 | 0.4 | Порог уверенности |
| `max-objects` | 5 | 15 | Максимальное число объектов |
| `margin` | 0.1 | 0.2 | Отступ вокруг бокса (доля) |

NMS выполняется с фиксированным IoU-порогом 0.45 (константа в коде, не конфигурируется). Семафор детекции (`libvips.detection.concurrency` / `max-wait`) настраивается в секции `libvips` — см. [CONFIGURATION.md](CONFIGURATION.md#libvips).

## Sidecar-кэш (filemeta)

Результаты детекции кэшируются в sidecar-метаданных (`domain/filemeta`):

- Боксы лиц (`faces`) и объектов (`objects`).
- `largest_ai_asset` — ссылка на наибольший AI-ассет (для инвалидации).
- `video_frame_key` — ключ извлечённого кадра видео.
- Фингерпринт источника: размер + mtime / SHA-256.

Sidecar-метаданные позволяют не пересчитывать детекцию при повторных запросах, если исходник не изменился.

## Влияние на производительность

- Детекция выполняется вне слота libvips (handoff), что не блокирует обработку других изображений.
- Семафор детекции ограничивает одновременные ONNX-инференсы.
- Ленивая загрузка моделей сокращает время старта.
- Graceful degradation гарантирует, что недоступность AI не ломает обработку: запросы продолжают обслуживаться с center-crop.

## Требования к сборке

- Тег сборки `onnx` (включён в `make build` по умолчанию: `TAGS=libvips onnx`).
- CGO + ONNX Runtime.
- Модели скачиваются скриптом `docker/download-models.sh` (или вручную в `models/`).

## Конфигурация

```yaml
detection:
  face-model: "/etc/imager/models/face_detection_yunet_2023mar.onnx"
  object-model: "/etc/imager/models/ssd_mobilenet_v1_12.onnx"
  confidence-threshold: 0.4
  max-objects: 15
  margin: 0.2

libvips:
  detection:
    concurrency: 0        # 0 = max(1, GOMAXPROCS/2)
    max-wait: "5s"
```

## Связанные документы

- [ARCHITECTURE.md](ARCHITECTURE.md) — место детекции в конвейере.
- [PROCESSING.md](PROCESSING.md) — операции кропа.
- [CONFIGURATION.md](CONFIGURATION.md) — секция `detection`.
- [FORMATS.md](FORMATS.md) — форматы и их ограничения.
- [DEVELOPMENT.md](DEVELOPMENT.md) — сборка с ONNX, тесты.