# Production Deployment Guide

Этот документ описывает production-запуск нового конвейера Imager
(composition root [`cmd/imager`](../cmd/imager/main.go)) поверх
`internal/adapters/httpapi`, `application/generatev2`, domain-пакетов и
storage-адаптеров.

> **Важно**: legacy entrypoint (`main.go`, `internal/app`, `internal/handler`,
> `internal/controller`) сохраняется для обратной совместимости, но **не
> является целевой точкой запуска**. Production-таргет — `cmd/imager`.

---

## 1. Запуск

### Docker Compose (рекомендуется)

```bash
docker compose up -d --build
```

Compose-файл уже содержит production hardening (см. раздел «Security»).
Конфигурация монтируется из каталога `./config` в `/etc/imager` (read-only);
внутри — `setting.yaml` (обязательный) и опциональный `setting-local.yaml`.

### Docker (вручную)

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

### Локально (без Docker)

Требуются **libvips** (для основного движка; сборка с `-tags libvips`) и
**FFmpeg**. ImageMagick — опционально (только для APNG). Конфигурация
читается из каталога, указанного в `IMAGER_CONFIG_DIR` (по умолчанию `.` —
корень репозитория, где лежат `setting.yaml`/`setting-local.yaml`).

```bash
# С libvips (основной движок; требует vips-dev + C-компилятор):
go build -tags libvips -trimpath -ldflags="-s -w" -o imager ./cmd/imager
IMAGER_CONFIG_DIR=. ./imager

# Без libvips (ImageMagick как primary; все форматы через ImageMagick):
go build -trimpath -ldflags="-s -w" -o imager ./cmd/imager
IMAGER_CONFIG_DIR=. ./imager
```

#### Windows: путь к ImageMagick

На Windows имя `magick` может не разрешаться через `PATH` процесса (например,
при запуске из IDE или службы), хотя ImageMagick установлен. Проверка:

```cmd
magick -version
```

Если команда не найдена — укажите абсолютный путь к `magick.exe` в
`setting-local.yaml` (используйте прямые слэши):

```yaml
imagemagick:
  binary: "D:/OSPanel/addons/ImageMagick-vs17/magick.exe"
```

После изменения перезапустите сервис. Проверка, что бинарь доступен:

```cmd
"D:/OSPanel/addons/ImageMagick-vs17/magick.exe" -version
```

---

## 2. Asset URL

Сервис принимает только канонические и preset URL:

```text
# Канонический
/{path}/{source_name}-{source_format}/{transform}-{size}@{dpr}.{output_format}

# Preset
/{path}/{source_name}-{source_format}/{preset_name}.{output_format}
```

- `transform` — ровно один из кодов: `c` (crop), `t` (trim), `ct` (trim затем
  crop). Комбинация `tc` и словесные значения (`crop`, `trim`) недопустимы.
- `dpr` — только `2` или `3`.
- Preset не содержит `source-format` в конфигурации: исходный формат
  определяется URL. `output-format` пресета обязан совпадать с расширением
  в URL.

Пример для исходника `test.jpg` (source name `test`, source format `jpg`):

```text
GET /test-jpg/c-120x80@2.webp        # канонический crop 120x80, dpr 2
GET /test-jpg/thumb.webp             # пресет "thumb" (crop 120x80, dpr 2, webp)
```

---

## 3. Конфигурация (YAML, без env)

**Все** настройки приложения задаются исключительно в YAML. Прикладных
env-переменных и CLI-флагов нет. Единственная env-переменная —
`IMAGER_CONFIG_DIR` — путь к каталогу с настройками:

| Env | По умолчанию | Описание |
|-----|--------------|----------|
| `IMAGER_CONFIG_DIR` | `.` | Каталог, где лежат `setting.yaml` и `setting-local.yaml`. |

Внутри каталога читаются:

- `setting.yaml` — **обязательный** базовый конфиг (отсутствие/невалидность —
  ошибка запуска);
- `setting-local.yaml` — **опциональный** локальный конфиг, который **глубоко
  переопределяет** базовый:
  - вложенные `map` мержатся (ключи, не указанные в local, сохраняются);
  - скаляры заменяются значением из local;
  - списки заменяются **целиком** (например `allowed-origins` или
    `disabled-coders` нельзя «дополнить» в local).

Неизвестные поля в любом из файлов отклоняются (strict decode, fail-fast).

### Схема YAML

```yaml
version: "1"
server:            # HTTP-сервер: addr, таймауты, max-header-bytes
http:              # HTTP-адаптер: CORS, cache-control, not-found и т.д.
watermarks:        # именованные декларации ватермарок (name/path/position/repeat/size)
policy:            # политика авторизации запросов
processing:        # умолчания обработки (default-quality и т.д.)
source:            # source-хранилище (storage, path, параметры backend)
result:            # result-хранилище (storage, path, параметры backend)
libvips:           # основной движок: limits (timeout, output-bytes, concurrency, threads, max-cache-*)
imagemagick:       # опциональный fallback для APNG: binary, policy.xml, resource limits
application:       # output-limit
observability:     # log-level
```

> **Движки обработки**: основной — libvips (govips, in-process; сборка с
> `-tags libvips`). ImageMagick — опциональный fallback только для **APNG**
> (единственный формат, который libvips не поддерживает). Если ImageMagick
> не установлен, запросы с форматом APNG возвращают HTTP 501 с понятным
> сообщением. Без тэка `libvips` сервис использует ImageMagick как primary
> (обратная совместимость).

Полный пример — в [`setting.yaml`](../setting.yaml) (с комментариями всех
полей). Подробное описание всех секций, параметров, дефолтов и ограничений,
а также примеры конфигураций — в [`README.md`](../README.md).

### Хранилища (source / result)

Source и result настраиваются **независимо** секциями `source:` и `result:`.
Тип задаётся ключом `storage` (`fs`, `s3`, `sftp`, `ftp`, `ftps`, `http`).
`fs` (или пустое значение) — локальный filesystem на `path`.

```yaml
source:
  storage: fs
  path: /var/www/site.ru/images
result:
  storage: fs
  path: /var/cache/imager
```

> **Важно**: и FTP, и FTPS поддерживают и source, и result. Публикация
> выполняется через temp-upload + rename и требует от сервера команд
> `STOR`, `RNFR`/`RNTO` и `DELE` (базовый RFC 959). Если сервер не
> поддерживает эти команды, `Publish` вернёт ошибку `ErrUnavailable`.

Общие ключи (применимы к обеим секциям):

| Ключ | По умолчанию | Описание |
|-----|--------------|----------|
| `storage` | `fs` | Тип хранилища: `fs`, `s3`, `sftp`, `ftp`, `ftps`, `http`. |
| `path` | `./data/source` / `./data/result` | Локальный каталог для `fs`. |
| `spool-dir` | `os.TempDir()` | Каталог временных spool при чтении remote-объектов. |
| `spool-max-bytes` | `0` (нет) | Лимит размера spool при чтении (превышение → quota error). |
| `dial-timeout` | `30s` | Таймаут соединения для SFTP/FTP/FTPS и HTTP-запросов (например `10s`). |

#### S3 (`storage: s3`)

| Ключ | По умолчанию | Описание |
|-----|--------------|----------|
| `bucket` | — | Имя bucket (**обязательно**). |
| `prefix` | — | Префикс ключей внутри bucket (опционально). |
| `endpoint` | AWS | Endpoint для S3-совместимых хранилищ (MinIO и т.п.). |
| `region` | — | Регион AWS. |
| `access-key` | — | Access key. |
| `secret-key` | — | Secret key. |

Если `access-key`/`secret-key` не заданы, используется стандартная цепочка
credentials AWS SDK (env/instance role и т.д.). S3 поддерживает и source, и
result. `NoOverwrite` реализуется через conditional PUT (`If-None-Match: "*"`).

#### SFTP (`storage: sftp`)

| Ключ | По умолчанию | Описание |
|-----|--------------|----------|
| `addr` | — | Адрес `host:port` (**обязательно**). |
| `user` | — | Пользователь (**обязательно**). |
| `password` | — | Пароль (password auth). |
| `private-key-file` | — | Путь к файлу приватного ключа (key auth). |
| `root` | — | Корневой каталог внутри SFTP (пусто = домашний каталог). |
| `host-key-fingerprint` | — | SHA-256 fingerprint host key (**обязательно**, например `SHA256:...`). |

Требуется хотя бы один метод аутентификации: `PASSWORD` или
`PRIVATE_KEY_FILE`. Поддерживает и source, и result. Result публикуется через
temp-upload + rename (атомарно); `NoOverwrite` — через эксклюзивное создание
(`O_EXCL`).

> **SSH host key**: для SFTP **обязательно** задать `host-key-fingerprint`
> (SHA-256 fingerprint, например `SHA256:...`). Без него конфигурация
> отклоняется на этапе валидации. Fingerprint можно получить командой
> `ssh-keyscan -t ed25519 host | ssh-keygen -lf -`.

#### FTPS (`ftps`) и FTP (`ftp`)

| Ключ | По умолчанию | Описание |
|-----|--------------|----------|
| `addr` | — | Адрес `host:port` (**обязательно**). |
| `user` | — | Пользователь (**обязательно**). |
| `password` | — | Пароль. |
| `root` | — | Корневой каталог (пусто = корень). |
| `tls` | `false` | Для `ftps` всегда `true` (explicit TLS, AUTH TLS). |
| `tls-verify` | `true` | Проверять TLS-сертификат (отключение запрещено). |

- **FTPS** (`ftps`): поддерживает и source, и result. Result публикуется через
  temp-upload + rename; `NoOverwrite` — best-effort проверка существования
  перед rename (не атомарно).
- **FTP** (`ftp`): поддерживает и source, и result (аналогично FTPS, но без
  TLS). Публикация требует команд `STOR`, `RNFR`/`RNTO` и `DELE`; при их
  отсутствии `Publish` вернёт `ErrUnavailable`. `NoOverwrite` — best-effort
  проверка существования перед rename (не атомарно).

> **TLS**: FTPS проверяет сертификат по умолчанию (`tls-verify: true`).
> Отключение проверки (`tls-verify: false`) **запрещено** на этапе валидации.
> Для самоподписанных сертификатов настройте доверенные CA в системе.

#### HTTP/HTTPS (`http`)

HTTP/HTTPS — **source-only** backend: он реализует только чтение исходников
и **не может** использоваться как result.

```yaml
source:
  storage: http
  base-url: "https://addr.site/path_to_image/"
```

Ключ объекта безопасно канонизируется и добавляется к базовому пути:

```text
base-url: https://addr.site/path_to_image/
key:      foo/bar.jpg
URL:      https://addr.site/path_to_image/foo/bar.jpg
```

Поведение:

- `Lookup` выполняется через `HEAD`, `Open` — через `GET`.
- **Redirects запрещены**: любой ответ `3xx` трактуется как
  `ErrUnavailable`.
- Статусы `404`/`410` → `ErrNotFound`; `401`/`403`, `408`, `429`, `5xx` и
  прочие non-2xx → `ErrUnavailable`.
- Размер скачиваемого объекта ограничивается `spool-max-bytes`
  (превышение → `ErrQuota`); при наличии `Content-Length` объект
  отклоняется до скачивания.
- Метаданные заполняются из `Content-Length`, `Last-Modified`,
  `Content-Type` и `ETag`.
- Таймаут запроса — `dial-timeout` (по умолчанию `30s`).
- `base-url` не должен содержать query-параметры или fragment (секреты в
  URL не поддерживаются).

---

## 3. Health endpoints

| Endpoint | Назначение |
|----------|------------|
| `/healthz` | Liveness. `200` пока процесс жив; `503` при shutdown. |
| `/readyz` | Readiness. `200` пока сервис принимает запросы; `503` при shutdown. |
| `/metrics` | Метрики в Prometheus exposition format (bounded cardinality). |
| `/debug/vars` | Сырые expvar-переменные (тот же источник, что `/metrics`). |

Healthcheck в Dockerfile/compose использует `/healthz`.

---

## 4. Observability

### Логи

Структурированные JSON-логи в **stderr** через `log/slog`. Каждый запрос
получает `request_id` (заголовок `X-Request-Id` или сгенерированный),
который пробрасывается в контекст и логи.

**Гарантии приватности**: URL/query/raw user input и секреты **не**
логируются и не попадают в метрики. Логируются только bounded-события
(статус-классы, ошибки по категориям, длительности).

### Метрики

Реализация — `internal/observability` на stdlib `expvar` (без внешних
зависимостей). Все label-ы — фиксированные enum-ы (bounded cardinality):

- `imager_requests{class}` — счётчик запросов по классу статуса (`2xx/3xx/4xx/5xx`).
- `imager_request_duration_seconds` — гистограмма длительности запросов.
- `imager_cache_hits` / `imager_cache_misses` — кэш-стадии.
- `imager_processor_success` / `imager_processor_errors` — стадия процессора.
- `imager_processor_duration_seconds` — гистограмма обработки.
- `imager_storage_ops{op}` — операции хранилища (`source_lookup`, `source_open`,
  `result_lookup`, `result_open`, `result_publish`) с суффиксом `_success`/`_error`.
- `imager_storage_duration_seconds_{op}` — гистограммы длительности storage ops.

`/metrics` отдаёт их в Prometheus exposition format для сбора.

---

## 5. Security assumptions

- **Non-root runtime**: контейнер запускается от `imager` (uid 10001).
- **Read-only root fs**: `read_only: true`; writable только `/data/source`,
  `/data/result` (volumes) и `/tmp` (tmpfs).
- **Dropped capabilities**: `cap_drop: ALL`, `cap_add: []`.
- **no-new-privileges**: `security_opt: no-new-privileges:true`.
- **Restrictive permissions**: бинарь `0755`, конфиг `0640`, каталоги `0750`.
- **ImageMagick deny-by-default policy**: `policy.xml` запрещает все coders/
  delegates и разрешает только безопасный whitelist; network-capable delegates
  отключены. Включается через `imagemagick.policy.enabled` в YAML.
- **Resource limits**: ImageMagick subprocess ограничен по памяти/времени/
  размеру выхода (`imagemagick.limits`); application-level bounded writer и
  context deadline.
- **HTTP hardening**: security headers, deny-by-default CORS, bounded URL length,
  таймауты сервера (read/write/idle/header), MaxHeaderBytes.
- **Секреты**: не логируются, не попадают в метрики, не хардкодятся в CI;
  задаются в `setting-local.yaml` (не в `setting.yaml`, который коммитится).

---

## 6. Resource limits

- **Compose**: `deploy.resources.limits` — `cpus: 2.0`, `memory: 512M`;
  reservations `cpus: 0.25`, `memory: 128M`.
- **ImageMagick**: лимиты из `imagemagick.limits` (memory/map/disk/threads/
  time/pixels/frames/output/timeout) применяются через `-limit`, policy.xml
  и application-level bounded writer + context deadline.
- **Application**: `application.output-limit` ограничивает размер выходного
  файла (дополнительно к `imagemagick.limits.output-bytes`).

---

## 7. Storage backends

Production-адаптеры реализуют `storage.SourceStore` / `storage.ResultStore` и
подключаются в composition root (`cmd/imager`) без изменения domain/
application слоёв. Порты определены в `internal/application/ports/storage`.

| Backend | Роль | Статус | Примечания |
|---------|------|--------|------------|
| Filesystem | Source + Result | ✅ Реализован | Атомарный publish (temp+rename). FS fallback по умолчанию. |
| S3 | Source + Result | ✅ Реализован | Conditional PUT (If-None-Match), ETag, multipart через AWS SDK v2. |
| SFTP | Source + Result | ✅ Реализован | Атомарный publish (temp+rename), `O_EXCL` для no-overwrite. |
| FTPS | Source + Result | ✅ Реализован | Explicit TLS (AUTH TLS); temp-upload + rename. |
| FTP | Source + Result | ✅ Реализован | temp-upload + rename; требует `STOR`/`RNFR`/`RNTO`/`DELE`. |
| HTTP/HTTPS | Source only | ✅ Реализован | HEAD/GET; redirects запрещены; лимит размера через spool. |
| External disk | Source + Result | 🔜 Roadmap | Может не поддерживать атомарный rename между каталогами. |

### Ограничения и безопасность

- **FTP/FTPS publish**: требует команд `STOR`, `RNFR`/`RNTO` и `DELE` (базовый
  RFC 959). При их отсутствии `Publish` вернёт `ErrUnavailable`. `NoOverwrite`
  — best-effort проверка существования перед rename (не атомарно).
- **SSH host key (SFTP)**: проверка host key **обязательна** — задаётся
  `host-key-fingerprint` (SHA-256). Без него конфигурация отклоняется.
- **TLS (FTPS)**: проверка сертификата **включена по умолчанию**
  (`tls-verify: true`); отключение запрещено на этапе валидации.
- **Секреты**: пароли, приватные ключи и S3 credentials задаются отдельными
  ключами в YAML (`password`, `private-key-file`, `access-key`, `secret-key`)
  и **не** попадают в URI/логи/метрики. Рекомендуется размещать их в
  `setting-local.yaml` (не коммитится, см. `.gitignore`).
- **Spool limit**: чтение remote-объектов идёт через ограниченный spool
  (`spool-max-bytes`); превышение → quota error.
- **Ключи**: нормализуются через `remote.CanonicalKey` (запрет `..`,
  обратных слешей, NUL) во всех remote-адаптерах.

---

## 8. CI

Workflow: [`.github/workflows/ci.yml`](../.github/workflows/ci.yml).

- `gofmt` check, `go vet`, `go test`, `go test -race`.
- Fuzz smoke: `FuzzParse`, `FuzzParseSize` (domain/asset), а также
  `FuzzCleanRelContainment`, `FuzzSafeKey` (storage/fs) — короткий timeout.
- Contract harness: `contract.Run` (ResultStore) и `contract.RunSource`
  (read-only SourceStore, пригоден для FTP) прогоняются для FS-адаптеров.
- Build `cmd/imager`.
- Container build + placeholder для scan (секреты — только через GitHub
  Secrets, никогда inline).

---

## 9. Quality gates (локально)

```bash
make fmt      # gofmt
make vet      # go vet ./...
make test     # go test ./...
make race     # go test -race ./...
make fuzz     # fuzz smoke
make build-prod
make docker-build
```
