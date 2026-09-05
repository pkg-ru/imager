# Production-развёртывание

## Запуск

### Docker Compose (рекомендуется)

```bash
docker compose up -d --build
```

Конфигурация монтируется из `./setting` в `/etc/imager/setting` read-only, каталог моделей `./models` — в `/etc/imager/models` **read-write**. Модели **не входят в образ** и **не требуют ручного размещения**: при старте контейнера entrypoint (`docker/entrypoint.sh`) скачивает их в смонтированный каталог (`docker/download-models.sh`, источники — OpenCV Zoo и ONNX Model Zoo по умолчанию) и сохраняет на хосте в `./models`, так что при перезапуске скачивание не повторяется. Если скачивание не удалось (нет сети/зеркала) — контейнер продолжает запуск с предупреждением: детекция опциональна, операции `fc`/`oc` просто недоступны (см. [CONFIGURATION.md](CONFIGURATION.md#detection)).

> **Права на каталог.** Контейнер работает от non-root `imager` (uid 10001). Чтобы entrypoint мог скачивать модели, сделайте хост-каталог `./models` доступным на запись этому uid: `chmod -R a+rwX ./models` (либо `chown 10001:10001 ./models`).

Env-переменные: `IMAGER_CONFIG_DIR=/etc/imager/setting` (каталог конфигурации) и `IMAGER_MODELS_DIR=/etc/imager/models` (каталог ONNX-моделей — **и** для автоскачивания, **и** fallback для пустых `detection.face-model`/`object-model`, см. [CONFIGURATION.md](CONFIGURATION.md#detection)). Опциональные `IMAGER_MODEL_FACE_URL` / `IMAGER_MODEL_OBJECT_URL` переопределяют источники скачивания (приватные зеркала), `IMAGER_SKIP_MODELS=1` отключает автоскачивание. Три слоя конфигурации (`setting`/`generate`/`failback`) описаны в [CONFIGURATION.md](CONFIGURATION.md#загрузка-конфигурации). Порт `8080`.

### Docker вручную

```bash
docker build -t imager:production .
docker run -d \
  --tmpfs /tmp:rw,noexec,nosuid,size=64m \
  --security-opt no-new-privileges:true \
  --cap-drop ALL \
  -p 8080:8080 \
  -v /host/setting:/etc/imager/setting:ro \
  -v /host/models:/etc/imager/models:rw \
  -v /host/source:/data/source:ro \
  -v /host/result:/data/result:rw \
  -e IMAGER_CONFIG_DIR=/etc/imager/setting \
  -e IMAGER_MODELS_DIR=/etc/imager/models \
  imager:production
```

### Без Docker

Сборка и зависимости — [INSTALLATION.md](INSTALLATION.md). Конфигурация читается из каталога `IMAGER_CONFIG_DIR` (по умолчанию текущий каталог).

## Укрепление контейнера (hardening)

| Мера | Реализация |
|------|------------|
| Non-root | Пользователь `imager` (uid 10001) в Dockerfile |
| Dropped capabilities | `cap_drop: ALL`, `cap_add: []` |
| no-new-privileges | `security_opt: no-new-privileges:true` |
| tmpfs | `/tmp`: `rw,noexec,nosuid,size=64m` |
| Права доступа | Бинарь `0755`, конфиг `0640`, каталоги данных `0750` |
| Pinned образы | `golang:1.27.0-alpine3.23` / `alpine:3.23`, pinned версии пакетов |
| Healthcheck | `wget http://127.0.0.1:8080/healthz` каждые 30s |

**`read_only: true` не используется.** При read-only rootfs Docker не может создать mountpoint для bind-mount `./models:/etc/imager/models` (каталог лежит в read-only слое). Writable-пути — bind-mounts `/data/result` (`:rw`), `/etc/imager/models` (`:rw`, сюда entrypoint скачивает модели) и tmpfs `/tmp`; `/data/source` и `/etc/imager/setting` монтируются `:ro`.

## Ресурсы

Compose-лимиты (`deploy.resources.limits`): `cpus: 2.0`, `memory: 512M`; reservations: `cpus: 0.25`, `memory: 128M`.

Подбирайте под нагрузку:

- `libvips.limits.concurrency` (рекомендуется 2–4) и `threads` (число логических ядер);
- `application.buffer-max-bytes` — бюджет памяти spillable-буферов;
- `http.max-concurrent-requests` — admission control при перегрузке.

## Graceful shutdown

По SIGINT/SIGTERM сервис:

1. прекращает принимать новые соединения;
2. дожидается активных запросов до `server.shutdown-timeout` (по умолчанию 15s);
3. дренирует очередь асинхронной публикации (см. [PROCESSING.md](PROCESSING.md#асинхронная-публикация)), закрывает хранилища, процессоры и пул буферов, останавливает janitor.

Compose использует `stop_signal: INT` и `stop_grace_period: 15s`.

## Health-check эндпоинты

| Эндпоинт | Назначение |
|----------|------------|
| `/healthz` | Liveness: `200 {"status":"alive"}`; `503` если процесс завершается |
| `/readyz` | Readiness: `200 {"status":"ready"}`; `503` при shutdown |
| `/metrics` | Метрики Prometheus exposition format |

Health/metrics остаются доступными при перегрузке asset-обработки (admission control применяется только к asset-запросам).

## nginx как фронт-прокси

Настройка описана в [NGINX.md](NGINX.md): раздача готовых файлов через `try_files`, проксирование генерации, проброс/скрытие эндпоинтов и выравнивание заголовков.

Ключевые моменты:

- **Раздача готовых файлов.** Ключ результата совпадает с путём в URL (без ведущего `/`), поэтому `try_files $uri @imager` с `root` на `result.path` отдаёт уже сгенерированные ассеты напрямую. Пути задаются в `source.path` / `result.path` (см. [STORAGE.md](STORAGE.md)).
- **Проксирование.** `proxy_pass` на адрес imager с пробросом `Host`, `X-Real-IP`, `X-Forwarded-For`, `X-Forwarded-Proto`; таймауты проксирования должны быть больше `http.generate-timeout` (по умолчанию 30s).
- **Эндпоинты.** В паблик пробрасываются asset URL (`/`), `/healthz`, `/readyz`. Служебные `/metrics` и `/admin/*` рекомендуется закрыть.

## Наблюдаемость

### Логи

Структурированные JSON-логи в stderr (`log/slog`). Каждый запрос получает request ID (заголовок `X-Request-Id` или сгенерированный), пробрасываемый в контекст и логи. URL/query/user input и секреты не логируются.

Уровень — `observability.log-level`: `debug`/`info`/`warn`/`error`; для production рекомендуется `info` или `warn`.

### Метрики

Реализация на stdlib `expvar`, отдаются через `/metrics` в Prometheus exposition format. Все метки — фиксированные enum-ы (bounded cardinality):

| Метрика | Описание |
|---------|----------|
| `imager_requests{class}` | Счётчик запросов по классу статуса (`2xx/3xx/4xx/5xx`) |
| `imager_request_duration_seconds` | Гистограмма длительности запросов |
| `imager_cache_hits` / `imager_cache_misses` | Стадии кэша |
| `imager_processor_success` / `imager_processor_errors` | Стадия процессора |
| `imager_processor_duration_seconds` | Гистограмма обработки |
| `imager_storage_ops{op}` | Операции хранилищ (`source_lookup/open`, `result_lookup/open/publish`) с исходами success/error |
| `imager_storage_duration_seconds_{op}` | Гистограммы длительности операций хранилищ |

### Защита /metrics

Опциональная защита `/metrics` по bearer-токену (`X-Metrics-Token`) и/или списку IP/CIDR настраивается в composition root. По умолчанию выключена; при публичном доступе ограничьте эндпоинт на уровне reverse-proxy или сети.

## Рекомендуемый production-профиль

**Три слоя конфигурации**, слияние и приоритеты — [CONFIGURATION.md](CONFIGURATION.md#загрузка-конфигурации). Секреты — только в `*-local.yaml` (не коммитятся).

`server-local.yaml` (фундамент; секреты не коммитятся):

```yaml
server:
  addr: ":8080"
  write-timeout: "120s"        # крупные медиа-ответы медленным клиентам

http:
  cache-control: "public, max-age=2592000"
  allowed-origins:
    - "https://cdn.example.com"
  max-concurrent-requests: 32

source:
  storage: s3
  bucket: "prod-images-source"
  prefix: "source/"
  endpoint: "https://storage.yandexcloud.net"
  region: "ru-central1"

result:
  storage: s3
  bucket: "prod-images-result"
  prefix: "gen/"

metadata:
  dir: "/var/cache/imager/meta"

libvips:
  limits:
    concurrency: 4
    threads: 4
    timeout: "30s"
    output-bytes: 10485760

application:
  buffer-max-bytes: 524288000
  limits:
    source-bytes: 10485760
    output-bytes: 10485760

observability:
  log-level: "warn"
```

`generate-local.yaml` (генерация ассетов — path-policies + application.limits):

```yaml
policy:
  presets:
    thumb:
      width: 200
      height: 200
      output-formats: [webp, avif]
      dpr: 1
  path-policies:
    "/":
      presets: ["thumb"]
      customs:
        x:
          output-formats: [webp]

application:
  limits:
    source-bytes: 10485760
    output-bytes: 10485760
```

Чек-лист перед запуском:

- [ ] `*-local.yaml` с секретами не коммитятся; секреты не в базовых `*.yaml`;
- [ ] настроены `policy.path-policies` (deny-by-default) и лимиты `application.limits`;
- [ ] `max-concurrent-requests` соответствует ресурсам контейнера;
- [ ] healthcheck балансировщика указывает на `/healthz` (liveness) и `/readyz` (readiness);
- [ ] `/metrics` закрыт от публичного доступа;
- [ ] `/data/source` смонтирован `:ro`, `/data/result` — на достаточный `:rw` volume; для fs-result работает janitor;
- [ ] TLS терминируется на reverse-proxy (сервис слушает plain HTTP).

## CI

Workflow: [`.gitverse/workflows/ci.yml`](../.gitverse/workflows/ci.yml).

- матрица build tags: `default`/`onnx` на Linux и Windows, `libvips`/`libvips,onnx` на Linux (CGO);
- `gofmt`, `go vet`, `go test`, `go test -race` (Linux);
- fuzz smoke: `FuzzParse`, `FuzzParseSize` (domain/asset), `FuzzCleanRelContainment` (storage/fs);
- `govulncheck`, сборка `cmd/imager`, container build и сканирование Trivy.
