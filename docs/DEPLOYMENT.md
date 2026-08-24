# Production-развёртывание

## Запуск

### Docker Compose (рекомендуется)

```bash
docker compose up -d --build
```

Конфигурация монтируется из `./config` в `/etc/imager` read-only; внутри — `setting.yaml` (обязательный) и опциональный `setting-local.yaml`. Порт `8080`.

### Docker вручную

```bash
docker build -t imager:production .
docker run -d \
  --read-only \
  --tmpfs /tmp:rw,noexec,nosuid,size=64m \
  --security-opt no-new-privileges:true \
  --cap-drop ALL \
  -p 8080:8080 \
  -v /host/config:/etc/imager:ro \
  -v imager_source:/data/source \
  -v imager_result:/data/result \
  -e IMAGER_CONFIG_DIR=/etc/imager \
  imager:production
```

### Без Docker

Сборка и зависимости — [INSTALLATION.md](INSTALLATION.md). Конфигурация читается из каталога `IMAGER_CONFIG_DIR` (по умолчанию текущий каталог).

## Hardening контейнера

| Мера | Реализация |
|------|------------|
| Non-root | Пользователь `imager` (uid/gid 10001) в Dockerfile |
| Read-only root fs | `read_only: true`; writable только `/data/source`, `/data/result` (volumes) и `/tmp` (tmpfs, noexec/nosuid) |
| Dropped capabilities | `cap_drop: ALL`, `cap_add: []` |
| no-new-privileges | `security_opt: no-new-privileges:true` |
| Права доступа | Бинарь `0755`, конфиг `0640`, каталоги данных `0750` |
| Pinned образы | `golang:1.25.0-alpine3.20` / `alpine:3.20`, pinned версии пакетов |
| Healthcheck | `wget http://127.0.0.1:8080/healthz` каждые 30s |

## Ресурсы

Compose-лимиты (`deploy.resources.limits`): `cpus: 2.0`, `memory: 512M`; reservations: `cpus: 0.25`, `memory: 128M`.

Подбирайте под нагрузку:

- `libvips.limits.concurrency` (рекомендуется 2–4) и `threads` (число логических ядер);
- `imagemagick.limits.concurrency` для fallback-сборок;
- `application.buffer-max-bytes` — бюджет памяти spillable-буферов;
- `http.max-concurrent-requests` — admission control при перегрузке.

## Graceful shutdown

По SIGINT/SIGTERM сервис:

1. прекращает принимать новые соединения;
2. дожидается активных запросов до `server.shutdown-timeout` (по умолчанию 15s);
3. закрывает хранилища, процессоры и пул буферов, останавливает janitor.

Compose использует `stop_signal: INT` и `stop_grace_period: 15s`.

## Health endpoints

| Endpoint | Назначение |
|----------|------------|
| `/healthz` | Liveness: `200 {"status":"alive"}`; `503` если процесс завершается |
| `/readyz` | Readiness: `200 {"status":"ready"}`; `503` при shutdown |
| `/metrics` | Метрики Prometheus exposition format |
| `/debug/vars` | Сырые expvar-переменные |

Health/metrics остаются доступными при перегрузке asset-обработки (admission control применяется только к asset-запросам).

## Observability

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

В composition root поддержана опциональная защита `/metrics` по bearer-токену (`X-Metrics-Token`) и/или списку IP/CIDR. По умолчанию защита выключена; при публичном доступе ограничьте endpoint на уровне reverse-proxy или сети.

## Рекомендуемый production-профиль

```yaml
# setting-local.yaml
server:
  addr: ":8080"
  write-timeout: "120s"        # крупные медиа-ответы медленным клиентам

http:
  cache-control: "public, max-age=2592000"
  allowed-origins:
    - "https://cdn.example.com"
  max-concurrent-requests: 32

policy:
  global:
    authorization: "safe"
    allowed-presets: ["thumb", "thumb@2"]
    size-rules: ["0-2000x0-2000"]
    limits:
      source-bytes: 10485760
      output-bytes: 10485760

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
  output-limit: 10485760
  buffer-max-bytes: 524288000

observability:
  log-level: "warn"
```

Чек-лист перед запуском:

- [ ] `setting-local.yaml` с секретами не коммитится; секреты не в `setting.yaml`;
- [ ] `authorization: "safe"` и настроены `allowed-presets`/`size-rules`;
- [ ] заданы лимиты `source-bytes`/`output-bytes` и `application.output-limit`;
- [ ] `max-concurrent-requests` соответствует ресурсам контейнера;
- [ ] healthcheck балансировщика указывает на `/healthz` (liveness) и `/readyz` (readiness);
- [ ] `/metrics` закрыт от публичного доступа;
- [ ] каталоги source/result смонтированы на достаточные volumes; для fs-result работает janitor;
- [ ] TLS терминируется на reverse-proxy (сервис слушает plain HTTP).

## CI

Workflow: `.github/workflows/ci.yml`.

- `gofmt`, `go vet`, `go test`, `go test -race`;
- fuzz smoke: `FuzzParse`, `FuzzParseSize` (domain/asset), `FuzzCleanRelContainment`, `FuzzSafeKey` (storage/fs);
- contract-тесты хранилищ (`contract.Run` для ResultStore, `contract.RunSource` для SourceStore) на FS-адаптерах;
- сборка `cmd/imager` и container build; сканирование образа; секреты — только через GitHub Secrets.
