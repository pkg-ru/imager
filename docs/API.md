# API

Сервис отдаёт изображения по каноническим (custom) и preset URL. Тела запросов не принимает (кроме админ-эндпоинтов, лимит 1 МБ); лимит тела обычных запросов — `server.max-body-bytes` (по умолчанию 4 KiB, превышение → `413`). Используются только методы `GET`, `HEAD`, `OPTIONS`.

Живой пример работы сервиса и клиентской части — [онлайн-демо](https://altuh.ru/demo/imager). Клиент для формирования asset URL и обращения к сервису — [imager-client](https://gitverse.ru/pkg-ru/imager-client).

## Эндпоинты

| Путь | Методы | Назначение |
|------|--------|------------|
| `/` (любой путь) | GET, HEAD, OPTIONS | Генерация и отдача ассета по asset URL |
| `/favicon.ico` | GET, HEAD | Встроенная иконка: `200`, `Content-Type: image/x-icon`, `Cache-Control: public, max-age=86400` |
| `/healthz` | GET, HEAD | Liveness: `200 {"status":"alive"}` / `503 {"status":"dead"}`; `Content-Type: application/json; charset=utf-8`, `Cache-Control: no-store` |
| `/readyz` | GET, HEAD | Readiness: `200 {"status":"ready"}` / `503 {"status":"not_ready"}`; те же заголовки |
| `/metrics` | GET | Метрики в Prometheus exposition format (может быть защищён токеном/IP — см. ниже и [DEPLOYMENT.md](DEPLOYMENT.md)) |
| `/admin/assets/generate` | POST | Фоновая генерация ассетов (только при `admin.enabled: true`) |
| `/admin/assets/delete` | DELETE | Удаление ассетов (только при `admin.enabled: true`) |

`/debug/vars` не регистрируется; все expvar-метрики доступны через `/metrics`.

`/metrics` отдаёт `Content-Type: text/plain; version=0.0.4; charset=utf-8` и `Cache-Control: no-store`. При настройке `server.metrics-auth` требуется заголовок `X-Metrics-Token` (constant-time сравнение SHA-256) и/или разрешённый IP (`allowed-ips`: точные адреса или CIDR); иначе — `403` с текстовым телом `forbidden` (не JSON envelope).

## Формат asset URL

```text
/{path}/{source_name}-{source_format}/{segment}@{dpr}.{output_format}
```

Единая грамматика для канонических и preset URL: `segment` — имя пресета (`policy.presets`) **или** custom-имя (размер-грамматика `x`, `x200`, `200x`, `200x200`), опционально с `@dpr`-суффиксом. 
Ведущий `/` необязателен; `{path}` может отсутствовать.

### Компоненты

| Компонент | Описание |
|-----------|----------|
| `path` | Логический путь исходника в хранилище (до 512 символов); запрещены `..`, `\`, `%2f`, encoded- и control-символы |
| `source_name` | Имя исходного файла без расширения; до 128 символов; любые Unicode-символы кроме `/`, `\`, `..`, control-символов |
| `source_format` | Формат исходника: `jpeg\|jpg\|png\|webp\|gif\|avif\|heif\|heic\|apng\|jxl`, а также видео `mp4\|webm\|mov\|mkv\|avi\|m4v` (ассеты из видео строятся из кадра — см. [PROCESSING.md](PROCESSING.md)). APNG-вход читается как анимированный PNG |
| `segment` | Имя пресета или custom-имя (≤64 символа; буквы, цифры, `_`, `.`, `@`, `-`, `!`, `,`). Дефисы допустимы, кроме имён, конфликтующих с грамматикой URL: `префикс-размер` (`sc-120x80`) и `имя-формат` (`my-png`) запрещены, обычные дефисы (`face-fix`) разрешены. Custom-имя — размер-грамматика: `120x80`, `x400`, `300x`, `x` (исходный размер) |
| `dpr` | Device pixel ratio; отсутствие = 1; явно допустимы только `2` и `3` (`@1`/`@0` — ошибка) |
| `output_format` | Выходной формат: `jpeg\|jpg\|png\|webp\|gif\|avif\|heif\|heic\|jxl`. APNG-выход не поддерживается (запись требует libvips с libspng). Запрос выхода, совпадающего с исходным форматом (включая видео `mp4`/`webm`/… и `svg`), на сегменте `x` отдаётся как passthrough без обработки |

Разрешение сегмента описано в [CONFIGURATION.md](CONFIGURATION.md#policy) (path-policies, deny-by-default).

### Правила разбора @dpr

- последний `@` — суффикс dpr URL; имя сегмента — всё до него;
- имя сегмента (пресет **или** custom) может содержать фиксированный `@dpr`-суффикс (`thumb@2`, `200x100@2`): тогда URL обязан содержать тот же `@dpr`. В конфигурации такой пресет/custom обязан иметь `dpr: N` (равный суффиксу) — иначе ошибка старта, см. [правила dpr](CONFIGURATION.md#правила-dpr);
- при разрешении сегмента с фиксированным dpr явный `@dpr` в URL, отличный от фиксированного, — ошибка;
- расширение в URL обязано совпадать с `output-formats` пресета/custom.

Полная матрица — в [CONFIGURATION.md](CONFIGURATION.md#правила-dpr).

## Примеры

Исходник `test.jpg` лежит в корне source-хранилища (`source_name=test`, `source_format=jpg`):

```bash
# Пресет thumb (200x200, если задан в path-policy "/")
curl -o thumb.webp http://localhost:8080/test-jpg/thumb.webp

# Пресет thumb@2 (dpr фиксирован именем)
curl -o thumb2.webp http://localhost:8080/test-jpg/thumb@2.webp

# Custom: ширина 640
curl -o out.webp http://localhost:8080/test-jpg/640x.webp

# Custom: только высота 400
curl -o out.png http://localhost:8080/test-jpg/x400.png

# Custom: исходный размер, конвертация в AVIF
curl -o out.avif http://localhost:8080/test-jpg/x.avif

# Custom 120x80@2 с DPR 2 (реально 240x160);
# в path-policy такой custom обязан иметь dpr: 2 (см. правила dpr)
curl -o out.webp http://localhost:8080/test-jpg/120x80@2.webp

# С путём: исходник thumbs/photo.jpg
curl -o out.webp http://localhost:8080/thumbs/photo-jpg/thumb.webp

# Условный запрос
curl -I -H "If-None-Match: \"etag-from-first-response\"" http://localhost:8080/test-jpg/thumb.webp   # 304
```

Примеры конфигурации пресетов/customs и path-policies — в [setting/generate.yaml](../setting/generate.yaml).

## Заголовки ответов

Успешный ассет:

| Заголовок | Значение |
|-----------|----------|
| `Content-Type` | MIME выходного формата (безопасный маппинг, не из запроса) |
| `Content-Length` | Размер файла |
| `Cache-Control` | Из `http.cache-control` (по умолчанию immutable на год) |
| `ETag` | Из метаданных хранилища либо SHA-256 от identity (canonical URL + size); кэшируется LRU |
| `Vary: Origin` | При наличии заголовка `Origin` в запросе |
| `X-Content-Type-Options: nosniff`, `Referrer-Policy` (по умолчанию `no-referrer`), `Content-Security-Policy` (если задан `http.csp`) | Security headers |
| `X-Request-Id` | Request ID: пробрасывается из одноимённого заголовка запроса или генерируется (16 байт hex) |

Поддерживаются условные запросы: `If-None-Match` со списком ETag или `*` → `304 Not Modified` без тела.

### Сжатие gzip

Gzip применяется только к JSON-ответам (error envelope, health) при `Accept-Encoding: gzip` (учитывается `q=0` как явный запрет). Изображения не сжимаются. При сжатии добавляются `Content-Encoding: gzip` и `Vary: Accept-Encoding`, `Content-Length` удаляется.

## Ошибки

Формат ошибки — стабильный JSON envelope:

```json
{"error": {"code": "not_found", "message": "not found"}}
```

| HTTP | code | Когда возникает |
|------|------|-----------------|
| `400` | `invalid` | Некорректный asset URL или запрос |
| `403` | `forbidden` | Запрос запрещён политикой или превышен лимит `application.limits` |
| `404` | `not_found` | Источник/результат не найден; применяется not-found fallback (`pixel`/`image`/`page`/`redirect`) |
| `405` | `method_not_allowed` | Метод отличен от GET/HEAD/OPTIONS (заголовок `Allow`) |
| `413` | — | Тело запроса больше `server.max-body-bytes` (текстовое тело, не JSON) |
| `414` | `invalid` | URL длиннее `http.max-url-len` |
| `431` | — | Заголовки больше `server.max-header-bytes` (текстовое тело, не JSON) |
| `500` | `processing` | Внутренняя ошибка обработки |
| `500` | `internal` | Паника в не-asset ветке (admin/health/metrics/static), перехваченная recover-middleware |
| `501` | `unsupported_format` | Формат/движок недоступен (например, fc/oc без ONNX) |
| `503` | `overloaded` / `unavailable` | Перегрузка процессоров (`Retry-After` из `http.retry-after`, по умолчанию `1`) или хранилище/координатор недоступны |
| `503` | — | Admission control (`http.max-concurrent-requests`): текстовое тело `too many requests` (не JSON) и динамический `Retry-After` (число занятых слотов, минимум 1) |
| `504` | `canceled` | Таймаут генерации (`http.generate-timeout`) или отмена клиента |
| `507` | `quota` | Превышена квота хранилища или лимит выходного файла |

Fallback-ответы и ошибки используют `Cache-Control` из `http.not-found-cache-control` (по умолчанию `no-store`).

### Source fallback

При ошибке ассета, когда **исходный файл существует**, сервис может отдать исходный файл. Включается секцией `http.source-fallback`; применяется к ошибкам: неканонический URL, несуществующий пресет, недопустимый план, запрещённая политика. `OutcomeNotFound` не покрывается. Статус и заголовки ответа — [CONFIGURATION.md](CONFIGURATION.md#httpsource-fallback).

Ответ source fallback содержит `Content-Type` (из метаданных хранилища или по расширению), `Content-Length`, `Content-Disposition: inline; filename="..."` (имя санитизируется до `[A-Za-z0-9._-]`), `Cache-Control` из `source-fallback.cache-control` и `ETag` из метаданных (если валиден).

### Serve original

Отдельная фича `http.serve-original` (не связана с source-fallback): «простые» URL вида `/path/name.ext` (без дефиса `name-format` в последнем сегменте) отдаются как прямые пути к исходнику со статусом `200` и `Cache-Control` из `serve-original.cache-control` (по умолчанию `no-store`). Выключена по умолчанию; при выключении такие URL дают `400 invalid`.

### Служебные пути (noise)

Запросы `favicon.ico`, `robots.txt`, `sitemap.xml`, `ads.txt`, `404.html`, `index.html`, `.well-known/*`, `.git/*` и т.п., не являющиеся валидными asset URL, обрабатываются тихо: лог на уровне DEBUG, без инкремента метрик ошибок и top-paths. `/favicon.ico` отдаётся встроенной иконкой (см. таблицу эндпоинтов).

### Admission control

`http.max-concurrent-requests` ограничивает число одновременно обрабатываемых asset-запросов (fallback — `application.limits.concurrency`). При переполнении — `503` с динамическим `Retry-After` (число занятых слотов, минимум 1). Health/metrics/admin не ограничиваются. Запрос, который может присоединиться к уже идущей singleflight-генерации того же ассета, пропускается в обход семафора (bypass) вместо `503`.

### Observability ошибок asset URL

Ошибки канонических URL/пресетов фиксируются при `observability.asset-errors.enabled: true` (по умолчанию включено):

- **структурные логи** с полями `kind` (`parse` | `preset_not_found` | `invalid_plan` | `policy_denied`), `url`, `preset`, `reason` на уровне `observability.asset-errors.log-level` (по умолчанию `warn`);
- **счётчик** `imager_asset_errors` — по категории `kind` (например `imager_asset_errors_parse`, `imager_asset_errors_preset_not_found`);
- **top bad paths** — при `observability.asset-errors.top-paths.enabled: true` bounded LRU-реестр проблемных путей (до `max-entries`, по умолчанию 1024). Ключ — путь исходника (`key-mode: source`) или sha256-хэш первых 16 байт URL (`key-mode: hash`). Реестр живёт в памяти процесса и в `/metrics` НЕ экспортируется (параметр `report-top`, по умолчанию 20, зарезервирован для программного отчёта).

Счётчики `imager_asset_errors_*` доступны в `/metrics` (expvar-реестр). Параметры секции — [CONFIGURATION.md](CONFIGURATION.md#observabilityasset-errors).

Метрики не содержат raw user input в unbounded виде: `url` — путь запроса без query, `preset` — имя пресета, `reason` — категория причины.

## CORS

Deny-by-default: cross-origin ответы получают CORS-заголовки только для origin из `http.allowed-origins`. `OPTIONS` возвращает `204` c `Allow: GET, HEAD, OPTIONS` и `Access-Control-Allow-Methods: GET, HEAD, OPTIONS`; при разрешённом origin отражаются `Access-Control-Allow-Origin`, (при `allow-credentials: true`) `Access-Control-Allow-Credentials` и (при наличии `Access-Control-Request-Headers`) отражённый `Access-Control-Allow-Headers`. Комбинация `"*"` + credentials запрещена конфигурацией.

## Админ-эндпоинты

Админ-эндпоинты управляют ассетами: фоновая генерация всех/выбранных ассетов исходника и удаление ассетов. Выключены по умолчанию (`admin.enabled: false`); при включении обязателен непустой `admin.token`, иначе fail-fast при старте. Параметры секции — [CONFIGURATION.md](CONFIGURATION.md#admin), рекомендации безопасности — [SECURITY.md](SECURITY.md#админ-эндпоинты).

Все админ-запросы требуют авторизации:

```
Authorization: Bearer <token>
```

Токен сравнивается через `crypto/subtle.ConstantTimeCompare` (constant-time). Неверный или отсутствующий токен → `403`:

```json
{"error": {"code": "forbidden", "message": "invalid or missing bearer token"}}
```

### POST /admin/assets/generate

Генерирует ассеты исходника. Тело — JSON. Задаётся **ровно одно** из полей `source` или `assets` (оба или ни одного — ошибка `400`).

#### Запрос

Режим A — генерация **всех** ассетов исходника по правилам политики и пресетам:

```json
{"source": "thumbs/photo.jpg"}
```

Режим B — генерация **только перечисленных** ассетов:

```json
{"assets": ["/thumbs/photo-jpg/thumb.webp", "/thumbs/photo-jpg/640x.webp"]}
```

Опциональное поле `wait`:

```json
{"source": "thumbs/photo.jpg", "wait": true}
```

| Поле | Тип | Описание |
|------|-----|----------|
| `source` | string | Путь исходника (например `thumbs/photo.jpg`); генерируются все его ассеты по правилам политики и пресетам. Существование проверяется **до** ответа (`404`, если нет). |
| `assets` | list[string] | Список канонических asset URL; исходники выводятся из URL и могут быть разными. |
| `wait` | bool | `false` (по умолчанию) — асинхронный режим, ответ `202` сразу после постановки в очередь. `true` — блокировать до завершения всех ассетов (с таймаутом `admin.wait-timeout`), ответ `200` после готовности. |

#### Ответы

**Асинхронный режим** (`wait` отсутствует или `false`) — `202 Accepted`:

```json
{"status": "accepted", "job_id": "a1b2c3d4e5f6a7b8", "queued": 12}
```

**Синхронный режим** (`wait: true`) — `200 OK` после завершения всех ассетов:

```json
{
  "status": "completed",
  "job_id": "a1b2c3d4e5f6a7b8",
  "queued": 12,
  "generated": 10,
  "skipped": 2,
  "failed": [
    {"url": "/thumbs/photo-jpg/thumb.webp", "code": "processing", "message": "..."}
  ]
}
```

| Поле | Тип | Описание |
|------|-----|----------|
| `status` | string | `"accepted"` (async) или `"completed"` (sync) |
| `job_id` | string | Случайный hex-идентификатор задачи (8 байт) |
| `queued` | int | Число ассетов, поставленных в очередь |
| `generated` | int | Число успешно сгенерированных ассетов (sync) |
| `skipped` | int | Число уже существующих ассетов, пропущенных без перегенерации (sync) |
| `failed` | list | Список неудавшихся ассетов: `url`, `code`, `message` (sync) |

Коды в `failed[].code` — категории исхода генерации (`invalid`, `forbidden`, `not-found`, `quota`, `unavailable`, `overloaded`, `processing`, `canceled`), а также `storage` (ошибка хранилища) и `error` (не классифицировано).

#### Коды ответов

| HTTP | code | Когда возникает |
|------|------|-----------------|
| `200` | — | Синхронный режим (`wait: true`), генерация завершена |
| `202` | — | Асинхронный режим, задача поставлена в очередь |
| `400` | `invalid` | Некорректный JSON, заданы оба/ни одного из `source`/`assets`, невалидный asset URL, политика не разрешает ни одного ассета для исходника (режим A) |
| `403` | `forbidden` | Неверный/отсутствующий bearer-токен |
| `404` | `not_found` | Исходник не существует (режим A) |
| `405` | `method_not_allowed` | Метод отличен от `POST` (заголовок `Allow: POST, DELETE`) |
| `503` | `overloaded` | Очередь задач переполнена (`admin.queue-size`) |
| `504` | `timeout` | Превышен таймаут режима `wait=true` (`admin.wait-timeout`) |

#### Примеры curl

```bash
# Асинхронная генерация всех ассетов исходника
curl -X POST http://localhost:8080/admin/assets/generate \
  -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  -d '{"source": "thumbs/photo.jpg"}'
# → 202 {"status":"accepted","job_id":"...","queued":12}

# Синхронная генерация выбранных ассетов
curl -X POST http://localhost:8080/admin/assets/generate \
  -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  -d '{"assets": ["/thumbs/photo-jpg/thumb.webp"], "wait": true}'
# → 200 {"status":"completed","job_id":"...","queued":1,"generated":1,"skipped":0}
```

### DELETE /admin/assets/delete

Удаляет ассеты. Тело запроса — JSON. Задаётся **ровно одно** из полей `source` или `assets`.

#### JSON-запрос

**Режим A** — удалить все ассеты исходника (кроме самого исходника):

```json
{"source": "thumbs/photo.jpg"}
```

Стратегия удаления (по приоритету): пакетное `DeleteByPrefix` (PrefixDeleter) → `List` + одиночный `Delete` (Lister) → «слепое» удаление по ключам, сформированным из политик и пресетов (без перечисления содержимого хранилища). `501` возможен, только если result-хранилище не задано вовсе.

**Режим B** — удалить перечисленные ассеты (канонические URL):

```json
{"assets": ["/thumbs/photo-jpg/thumb.webp", "/thumbs/photo-jpg/640x.webp"]}
```

| Поле | Тип | Описание |
|------|-----|----------|
| `source` | string | Путь исходника |
| `assets` | list[string] | Список канонических asset URL |

#### Ответ

`200 OK`:

```json
{"status": "completed", "deleted": 3}
```

| Поле | Тип | Описание |
|------|-----|----------|
| `status` | string | `"completed"` |
| `deleted` | int | Число удалённых ассетов |

#### Коды ответов

| HTTP | code | Когда возникает |
|------|------|-----------------|
| `200` | — | Удаление выполнено |
| `400` | `invalid` | Некорректный JSON, заданы оба/ни одного из `source`/`assets`, невалидный asset URL или source-путь |
| `403` | `forbidden` | Неверный/отсутствующий bearer-токен |
| `413` | `too_large` | Тело запроса превышает 1 МБ |
| `501` | `not_implemented` | Result-хранилище не задано (режим A) |
| `500` | `internal` | Внутренняя ошибка |

#### Примеры curl

```bash
# Удалить все ассеты исходника
curl -X DELETE http://localhost:8080/admin/assets/delete \
  -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  -d '{"source": "thumbs/photo.jpg"}'
# → 200 {"status":"completed","deleted":3}

# Удалить перечисленные ассеты
curl -X DELETE http://localhost:8080/admin/assets/delete \
  -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  -d '{"assets": ["/thumbs/photo-jpg/thumb.webp"]}'
# → 200 {"status":"completed","deleted":1}
