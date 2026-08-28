# API

Сервис отдаёт изображения по каноническим и preset URL. Тела запросов не принимает; используются только методы `GET`, `HEAD`, `OPTIONS`.

## Эндпоинты

| Путь | Методы | Назначение |
|------|--------|------------|
| `/` (любой путь) | GET, HEAD, OPTIONS | Генерация и отдача ассета по asset URL |
| `/healthz` | GET | Liveness: `200 {"status":"alive"}` / `503 {"status":"dead"}` |
| `/readyz` | GET | Readiness: `200 {"status":"ready"}` / `503 {"status":"not_ready"}` |
| `/metrics` | GET | Метрики в Prometheus exposition format (может быть защищён токеном/IP — см. [DEPLOYMENT.md](DEPLOYMENT.md)) |
| `/debug/vars` | GET | Сырые expvar-переменные |
| `/admin/assets/generate` | POST | Фоновая генерация ассетов (только при `admin.enabled: true`) |
| `/admin/assets/delete` | DELETE | Удаление ассетов (только при `admin.enabled: true`) |

Подробности админ-эндпоинтов — в разделе [Админ-эндпоинты](#админ-эндпоинты); требования безопасности — [SECURITY.md](SECURITY.md).

## Формат asset URL

```text
Канонический:
/{path}/{source_name}-{source_format}/{transform}-{size}@{dpr}.{output_format}

Канонический без transform (resize):
/{path}/{source_name}-{source_format}/{size}@{dpr}.{output_format}

Preset:
/{path}/{source_name}-{source_format}/{preset_name}@{dpr}.{output_format}
```

Ведущий `/` необязателен. `{path}` может отсутствовать.

### Компоненты

| Компонент | Описание |
|-----------|----------|
| `path` | Логический путь исходника в хранилище; запрещены `..`, `%2f`, control-символы |
| `source_name` | Имя исходного файла без расширения; до 128 символов; любые Unicode-символы кроме `/`, `\`, `..`, control-символов |
| `source_format` | Формат исходника: `jpeg\|jpg\|png\|webp\|gif\|avif\|heif\|heic\|apng\|jxl` |
| `transform` | Код операции (см. таблицу ниже); необязателен |
| `size` | Целевой размер: `120x80`, `x400` (только высота), `300x` (только ширина), `x` (исходный размер) |
| `dpr` | Device pixel ratio; отсутствие = 1; явно допустимы только `2` и `3` (`@1`/`@0` — ошибка) |
| `output_format` | Выходной формат: `jpeg\|jpg\|png\|webp\|gif\|avif\|heif\|heic\|apng\|jxl` |
| `preset_name` | Имя пресета из конфигурации; до 64 символов, без дефисов; допустимы буквы, цифры, `_`, `.`, `@` |

### Transform-коды

| Код | Операция |
|-----|----------|
| *(отсутствует)* | Resize под заданный размер |
| `c` | Центрированный crop |
| `t` | Только trim (обрезка однотонных полей), затем resize |
| `ct` | Trim → центрированный crop |
| `sc` | Smart-crop (attention-область; требует `-tags libvips`) |
| `sct` | Trim → smart-crop |
| `fc` | Face-crop (детекция лиц; требует `-tags libvips,onnx` и настроенные модели) |
| `fct` | Trim → face-crop |
| `oc` | Object-crop (детекция объектов; требует `-tags libvips,onnx` и модели) |
| `oct` | Trim → object-crop |

Trim всегда применяется первым; координаты детекции/attention относятся к уже подрезанному изображению. Комбинации вида `tc` недопустимы.

### Правила разбора @dpr

- канонический URL (`transform-size` или `size`): последний `@` — суффикс dpr URL;
- preset URL с одним `@`: это часть имени пресета (`thumb@2`), dpr URL = 1;
- preset URL с двумя `@` (`thumb@2@3`): последний `@` — dpr URL, имя пресета — всё до него.

При разрешении пресета с фиксированным dpr явный `@dpr` в URL, отличный от фиксированного, — ошибка. Расширение в URL обязано совпадать с `output-formats` пресета.

## Примеры

Исходник `test.jpg` лежит в корне source-хранилища (`source_name=test`, `source_format=jpg`):

```bash
# Resize до ширины 640
curl -o out.webp http://localhost:8080/test-jpg/640x.webp

# Resize только по высоте 400
curl -o out.png http://localhost:8080/test-jpg/x400.png

# Исходный размер, конвертация в AVIF
curl -o out.avif http://localhost:8080/test-jpg/x.avif

# Центрированный crop 120x80, DPR 2 (реально 240x160)
curl -o out.webp http://localhost:8080/test-jpg/120x80@2.webp

# Trim + центрированный crop 300x300
curl -o out.avif http://localhost:8080/test-jpg/ct-300x300.avif

# Smart-crop 800x600
curl -o out.webp http://localhost:8080/test-jpg/sc-800x600.webp

# Face-crop 300x300 с DPR 3
curl -o out.jpeg http://localhost:8080/test-jpg/fc-300x300@3.jpeg

# Пресеты из config/generate.yaml
curl -o thumb.webp http://localhost:8080/test-jpg/thumb.webp      # 200x200 WebP
curl -o thumb2.webp http://localhost:8080/test-jpg/thumb@2.webp   # 400x400 WebP

# С путём: исходник thumbs/photo.jpg
curl -o out.webp http://localhost:8080/thumbs/photo-jpg/thumb.webp

# Условный запрос
curl -I -H "If-None-Match: \"etag-from-first-response\"" http://localhost:8080/test-jpg/thumb.webp   # 304
```

## Заголовки ответов

Успешный ассет:

| Заголовок | Значение |
|-----------|----------|
| `Content-Type` | MIME выходного формата (безопасный маппинг, не из запроса) |
| `Content-Length` | Размер файла |
| `Cache-Control` | Из `http.cache-control` (по умолчанию immutable на год) |
| `ETag` | Из метаданных хранилища либо SHA-256 от identity (canonical URL + size); кэшируется LRU |
| `Vary: Origin` | При наличии заголовка `Origin` в запросе |
| `X-Content-Type-Options: nosniff`, `Referrer-Policy` | Security headers |

Поддерживаются условные запросы: `If-None-Match` со списком ETag или `*` → `304 Not Modified` без тела.

## Ошибки

Формат ошибки — стабильный JSON envelope:

```json
{"error": {"code": "not_found", "message": "not found"}}
```

| HTTP | code | Когда возникает |
|------|------|-----------------|
| `400` | `invalid` | Некорректный asset URL или запрос |
| `403` | `forbidden` | Запрос запрещён политикой или превышен лимит политики |
| `404` | `not_found` | Источник/результат не найден; применяется not-found fallback (`pixel`/`image`/`page`/`redirect`) |
| `405` | `method_not_allowed` | Метод отличен от GET/HEAD/OPTIONS (заголовок `Allow`) |
| `414` | `invalid` | URL длиннее `http.max-url-len` |
| `431` | — | Заголовки больше `server.max-header-bytes` |
| `500` | `processing` | Внутренняя ошибка обработки |
| `501` | `unsupported_format` | Формат/движок недоступен (например, fc/oc без ONNX) |
| `503` | `overloaded` / `unavailable` | Перегрузка процессоров/admission control (`Retry-After: 1`) или хранилище недоступно |
| `504` | `canceled` | Таймаут генерации (`http.generate-timeout`) или отмена клиента |
| `507` | `quota` | Превышена квота хранилища или лимит выходного файла |

Fallback-ответы и ошибки используют `Cache-Control` из `http.not-found-cache-control` (по умолчанию `no-store`).

### Source fallback

При ошибке ассета, когда **исходный файл существует**, сервис может отдать исходный файл вместо пикселя/ошибки. Включается секцией `http.source-fallback` (см. [CONFIGURATION.md](CONFIGURATION.md#httpsource-fallback)). По умолчанию выключен.

Fallback применяется к следующим ошибкам:

- **неканонический URL** (ошибка разбора `parse`);
- **несуществующий пресет** (`preset_not_found`);
- **недопустимый план** (`invalid_plan`);
- **запрещённая политика** (`policy_denied`).

`OutcomeNotFound` (исходника нет) **не** покрывается source fallback — в этом случае применяется обычный not-found fallback (`pixel`/`image`/`page`/`redirect`).

Когда fallback срабатывает, вместо JSON-ошибки отдаётся исходный файл с его оригинальными заголовками:

| Заголовок | Значение |
|-----------|----------|
| `Content-Type` | Из метаданных исходника, иначе по расширению, иначе `application/octet-stream` |
| `Content-Length` | Размер исходного файла |
| `Content-Disposition` | `inline; filename="<name>.<format>"` |
| `Cache-Control` | Из `http.source-fallback.cache-control` (по умолчанию `no-store`) |
| `ETag` | Из метаданных исходника (если есть) |

HTTP-статус ответа задаётся `http.source-fallback.status` — `200` или `404` (по умолчанию `404`). Выбор `200` означает, что CDN/браузеры будут кэшировать ответ как успешный; `404` — как ошибочный. Подробнее о выборе — в [CONFIGURATION.md](CONFIGURATION.md#httpsource-fallback).

### Observability ошибок asset URL

Ошибки канонических URL/пресетов (неканонический URL, несуществующий пресет, недопустимый план, запрещённая политика) фиксируются при `observability.asset-errors.enabled: true` (по умолчанию включено):

- **структурные логи** с полями `kind` (`parse` | `preset_not_found` | `invalid_plan` | `policy_denied`), `url`, `preset`, `reason` на уровне `observability.asset-errors.log-level` (по умолчанию `warn`);
- **счётчик** `imager_asset_errors` в `/metrics` и `/debug/vars` — по категории `kind` (например `imager_asset_errors_parse`, `imager_asset_errors_preset_not_found`);
- **top bad paths** — при `observability.asset-errors.top-paths.enabled: true` bounded LRU-реестр проблемных путей (до `max-entries`, по умолчанию 1024) с отчётом топ-`report-top` (по умолчанию 20) путей. Ключ — путь исходника (`key-mode: source`) или sha256-хэш первых 16 байт URL (`key-mode: hash`). Отчёт публикуется в `/debug/vars` (expvar) и доступен в `/metrics`.

Метрики не содержат raw user input в unbounded виде: `url` — путь запроса без query, `preset` — имя пресета, `reason` — категория причины.

## CORS

Deny-by-default: cross-origin ответы получают CORS-заголовки только для origin из `http.allowed-origins`. `OPTIONS` возвращает `204` c `Allow: GET, HEAD, OPTIONS`; при разрешённом origin отражаются `Access-Control-Allow-Origin` и (при `allow-credentials: true`) `Access-Control-Allow-Credentials`. Комбинация `"*"` + credentials запрещена конфигурацией.

## Админ-эндпоинты

Админ-эндпоинты управляют ассетами: фоновая генерация всех/выбранных ассетов исходника и удаление ассетов. Они **выключены по умолчанию** и регистрируются в mux только при `admin.enabled: true` (см. [CONFIGURATION.md](CONFIGURATION.md#admin)). При включении обязателен непустой `admin.token`, иначе старт завершится ошибкой (fail-fast).

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

#### Коды ответов

| HTTP | code | Когда возникает |
|------|------|-----------------|
| `200` | — | Синхронный режим (`wait: true`), генерация завершена |
| `202` | — | Асинхронный режим, задача поставлена в очередь |
| `400` | `invalid` | Некорректный JSON, заданы оба/ни одного из `source`/`assets`, невалидный asset URL, `cannot-enumerate` (например, `unsafe` authorization без `size-rules`) |
| `403` | `forbidden` | Неверный/отсутствующий bearer-токен |
| `404` | `not_found` | Исходник не существует (режим A) |
| `501` | `not_implemented` | Хранилище результатов не поддерживает перечисление (не применимо к generate) |
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

Работает для всех хранилищ (fs, s3, ftp, sftp): используется пакетное
`DeleteByPrefix` (PrefixDeleter), а при его отсутствии — fallback на
`List` + одиночный `Delete`. Если хранилище не поддерживает ни то, ни
другое — `501`.

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
| `400` | `invalid` | Некорректный JSON, заданы оба/ни одного из `source`/`assets`, невалидный asset URL |
| `403` | `forbidden` | Неверный/отсутствующий bearer-токен |
| `413` | `too_large` | Тело запроса превышает 1 МБ |
| `501` | `not_implemented` | Result-хранилище не поддерживает ни `DeleteByPrefix`, ни `list` (режим A) |
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
```
