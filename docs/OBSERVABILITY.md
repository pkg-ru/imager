# Наблюдаемость

Imager предоставляет метрики, структурированные логи и health-эндпоинты для мониторинга в production.

Связанные документы: [ARCHITECTURE.md](ARCHITECTURE.md), [CONFIGURATION.md](CONFIGURATION.md), [DEPLOYMENT.md](DEPLOYMENT.md), [TROUNLESHOOTING.md](TROUNLESHOOTING.md).

## Метрики

Метрики собираются через expvar и экспортируются в Prometheus exposition format на эндпоинте `/metrics`.

### Основные метрики

| Метрика | Тип | Описание |
|---------|-----|----------|
| `imager_requests` | counter | Общее число запросов |
| `imager_request_duration_seconds` | histogram | Длительность запросов |
| `imager_cache_hits` / `imager_cache_misses` | counter | Попадания/промахи кэша |
| `imager_processor_success` / `imager_processor_errors` | counter | Успехи/ошибки обработки |
| `imager_processor_duration_seconds` | histogram | Длительность обработки |
| `imager_storage_ops` | counter | Операции хранилищ |
| `imager_storage_duration_seconds_*` | histogram | Длительность операций хранилищ |
| `imager_detection_degraded_total` | counter | Число деградаций детекции |
| `imager_vips_orphan_ops_total` / `imager_vips_orphan_inflight` | counter/gauge | «Осиротевшие» операции libvips |
| `imager_panics_total` | counter | Паники |
| `imager_http_inflight` | gauge | In-flight HTTP-запросы |
| `imager_singleflight_keys` | gauge | Число ключей singleflight |
| `imager_buffer_pool_bytes` | gauge | Байты в пуле буферов |
| `imager_cache_evictions_total` / `imager_cache_entries` | counter/gauge | Вытеснения/записи кэша |
| `imager_publish_queue_depth` | gauge | Глубина очереди публикации |
| `imager_publish_errors_total` | counter | Ошибки публикации |
| `imager_asset_errors` | counter | Ошибки ассетов (по типам) |
| `imager_vips_tracked_memory_bytes` | gauge | Отслеживаемая память libvips |
| `imager_vips_tracked_allocs` | gauge | Число аллокаций libvips |
| `imager_vips_open_files` | gauge | Открытые файлы libvips |
| `imager_vips_mem_highwater_bytes` | gauge | High-water mark памяти libvips |
| `imager_vips_operations_total` | counter | Операции libvips |
| `imager_vips_watermark_cache_*` | gauge | Кэш водяных знаков |

### Гистограммы

Гистограммы длительности имеют бакеты: `[0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5, 10]` секунд.

### Bounded cardinality

Метрики имеют ограниченную кардинальность (bounded cardinality), чтобы избежать взрыва числа уникальных метрик:

- Классы статусов: `2xx`, `3xx`, `4xx`, `5xx`.
- Операции хранилищ: фиксированный набор (`StorageOp`).
- Типы ошибок ассетов: фиксированный набор (`AssetErrorKind`).

### Top-paths

`observability/toppaths.go` — LRU-реестр самых частых путей:

- `max-entries`: 1024.
- `report-top`: 20.
- `key-mode`: `source` | `hash`.

## Логи

- Формат: slog, JSON в stderr.
- Уровень: `log-level` (например, `info`).
- Request ID: заголовок `X-Request-Id` (16 байт hex, генерируется из криптографического RNG). Логи запроса содержат request ID для трассировки.

## Health-эндпоинты

| Эндпоинт | Тип | Описание |
|----------|-----|----------|
| `/healthz` | liveness | Сервис жив (процесс работает) |
| `/readyz` | readiness | Сервис готов принимать трафик |

## Middleware-порядок

HTTP-конвейер (`adapters/httpapi/mux.go`):

```
Recover → observability → gzip → mux
```

1. **Recover** — перехват ошибок, JSON-обёртка 500.
2. **Observability** — сбор метрик и request ID.
3. **Gzip** — сжатие ответов.
4. **Mux** — маршрутизация (ассеты, health, метрики, admin).

## Защита метрик

Эндпоинт `/metrics` защищён (секция `server.metrics-auth`):

- Токен: заголовок `X-Metrics-Token`, SHA-256 + constant-time compare.
- CIDR: `allowed-ips` — список разрешённых IP/подсетей.

## Vips-метрики

`observability/vips_metrics.go` — периодический сборщик (интервал 15 секунд) метрик libvips: tracked memory/allocs, open files, high-water mark, операции. Провайдер подключается через `SetVipsStatsProvider`.

## Ошибки ассетов

`observability/events.go` — событие `AssetErrorEvent` и `LogAssetError`: логирование ошибок ассетов с типом (`AssetErrorKind`) и путём. Метрика `imager_asset_errors` агрегирует по типам.

## Конфигурация

```yaml
observability:
  log-level: info
  asset-errors:
    enabled: true
    log-level: warn
    top-paths:
      enabled: false
      max-entries: 1024
      report-top: 20
      key-mode: source   # source | hash

server:
  metrics-auth:
    token: "<sha256-hex>"        # заголовок X-Metrics-Token
    allowed-ips: ["127.0.0.1/32"]
```

## Связанные документы

- [ARCHITECTURE.md](ARCHITECTURE.md) — место наблюдаемости в архитектуре.
- [CONFIGURATION.md](CONFIGURATION.md) — секция `observability`.
- [DEPLOYMENT.md](DEPLOYMENT.md) — настройка мониторинга в production.
- [TROUNLESHOOTING.md](TROUNLESHOOTING.md) — диагностика по метрикам и логам.