# Диагностика проблем

Справочник по типичным проблемам Imager: коды ошибок, проблемы libvips, ONNX, хранилищ, памяти и производительности.

Связанные документы: [API.md](API.md), [OBSERVABILITY.md](OBSERVABILITY.md), [CONFIGURATION.md](CONFIGURATION.md), [STORAGE.md](STORAGE.md), [AI.md](AI.md).

## HTTP-коды ошибок

| Код | Outcome | Причина | Диагностика |
|-----|---------|---------|-------------|
| 400 | `invalid` | Некорректный URL/запрос | Проверьте формат URL ассета |
| 403 | `forbidden` | Запрещено политикой | Проверьте [POLICIES.md](POLICIES.md) |
| 404 | `not_found` | Исходник не найден | Проверьте source-хранилище |
| 405 | `method_not_allowed` | Неверный метод | Используйте GET |
| 413 | — | Тело запроса слишком велико | Проверьте `server.max-body-bytes` |
| 414 | `invalid` | URL слишком длинный | Максимум 1024 байта |
| 431 | — | Заголовки слишком велики | Проверьте заголовки |
| 500 | `processing` / `internal` | Ошибка обработки | Смотрите логи (slog JSON) |
| 501 | `unsupported_format` | Формат не поддерживается | Проверьте [FORMATS.md](FORMATS.md) |
| 503 | `overloaded` / `unavailable` | Перегрузка / недоступность | Проверьте admission control и семафоры |
| 504 | `canceled` | Таймаут/отмена | Проверьте таймауты |
| 507 | `quota` | Превышен лимит | Проверьте лимиты обработки |

## Проблемы libvips

### Сервис не стартует: libvips не найден

- Убедитесь, что libvips ≥ 8.16 установлен.
- Сборка с тегом `libvips` (`make build`).
- Для Alpine/musl: `CGO_LDFLAGS="-no-pie"`.

### Ошибки кодирования формата

- Проверьте, что формат поддерживается libvips (`vips --list formats`).
- APNG-запись требует libspng (см. [FORMATS.md](FORMATS.md)).
- Анимированный HEIF/HEIC не поддерживается (баг libheif 1.23) — используйте AVIF.

### «Осиротевшие» операции (orphan ops)

- Метрики `imager_vips_orphan_ops_total` / `imager_vips_orphan_inflight` показывают зависшие операции.
- Watchdog (`runWatchdog`) завершает операции по таймауту.
- Проверьте, не превышен ли `libvips.limits.timeout`.

## Проблемы ONNX / AI

### Детекция не работает (graceful degradation)

- Проверьте, что сборка с тегом `onnx`.
- Проверьте пути к моделям (`detection.face-model` / `detection.object-model`).
- Модели скачиваются `docker/download-models.sh` (или вручную в `models/`).
- Метрика `imager_detection_degraded_total` растёт — детекция деградирует до center-crop.

### Медленная детекция

- Проверьте `libvips.detection.concurrency` (по умолчанию `max(1, GOMAXPROCS/2)`).
- Проверьте `libvips.detection.max-wait` (5 секунд).
- Убедитесь, что sidecar-кэш (filemeta) работает — повторные запросы не должны пересчитывать детекцию.

## Проблемы хранилищ

### Исходник не найден (404)

- Проверьте source-хранилище и ключи.
- Для `http`-источников: проверьте `spool-max-bytes` (по умолчанию 512 MiB).
- Проверьте, что `source-fallback` включён, если нужен fallback на оригинал.

### Ошибки публикации

- Метрика `imager_publish_errors_total` растёт.
- Проверьте result-хранилище (права, квоты).
- Публикация асинхронная: bounded-очередь 512, 4 воркера, drain 5 секунд, синхронный fallback 30 секунд.

### Remote-хранилища медленные

- Пул буферов: `application.buffer-max-bytes` (по умолчанию 500 MiB) — spill на диск при превышении.
- Проверьте метрики `imager_storage_duration_seconds_*`.

## Проблемы памяти

### OOM / высокое потребление памяти

- Проверьте `libvips.limits.concurrency` (shipped: 8) — слоты ограничивают параллельную обработку.
- Проверьте `source-bytes` / `output-bytes` лимиты.
- Метрики vips: `imager_vips_tracked_memory_bytes`, `imager_vips_mem_highwater_bytes`.
- Проверьте `application.limits` (pixels, frames, duration).

### Утечка памяти

- Следите за `imager_vips_tracked_allocs` и `imager_vips_open_files`.
- Проверьте, что все буферы закрываются (refcount в remote-буферах).

## Проблемы производительности

### Высокая задержка

- Гистограммы: `imager_request_duration_seconds`, `imager_processor_duration_seconds`.
- Проверьте admission control: `imager_http_inflight`, `http.max-concurrent-requests`.
- Проверьте singleflight: `imager_singleflight_keys` (максимум 16384).
- Проверьте кэш: `imager_cache_hits` / `imager_cache_misses`.

### Перегрузка (503)

- `Retry-After` показывает число занятых слотов.
- Увеличьте `http.max-concurrent-requests` или `application.limits.concurrency`.
- Проверьте, что singleflight-join обходит admission control (bypass).

## Диагностика по логам

- Логи: slog JSON в stderr.
- Request ID: `X-Request-Id` (16 байт hex) — трассировка запроса по логам.
- Ошибки ассетов: `imager_asset_errors` + событие `AssetErrorEvent`.

## Диагностика health

- `/healthz` — liveness (процесс жив).
- `/readyz` — readiness (готов принимать трафик).
- Если `/readyz` возвращает ошибку — проверьте инициализацию хранилищ и моделей.

## Связанные документы

- [API.md](API.md) — коды ошибок и формат URL.
- [OBSERVABILITY.md](OBSERVABILITY.md) — метрики и логи.
- [CONFIGURATION.md](CONFIGURATION.md) — параметры.
- [STORAGE.md](STORAGE.md) — хранилища.
- [AI.md](AI.md) — AI-детекция.
- [FORMATS.md](FORMATS.md) — форматы.
- [DEVELOPMENT.md](DEVELOPMENT.md) — сборка и тесты.