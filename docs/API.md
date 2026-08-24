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

При разрешении пресета с фиксированным dpr явный `@dpr` в URL, отличный от фиксированного, — ошибка. Расширение в URL обязано совпадать с `output-format` пресета.

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
curl -o out.webp http://localhost:8080/test-jpg/c-120x80@2.webp

# Trim + центрированный crop 300x300
curl -o out.avif http://localhost:8080/test-jpg/ct-300x300.avif

# Smart-crop 800x600
curl -o out.webp http://localhost:8080/test-jpg/sc-800x600.webp

# Face-crop 300x300 с DPR 3
curl -o out.jpeg http://localhost:8080/test-jpg/fc-300x300@3.jpeg

# Пресеты из config/setting.yaml
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
| `501` | `unsupported_format` | Формат/движок недоступен (например fc/oc без ONNX) |
| `503` | `overloaded` / `unavailable` | Перегрузка процессоров/admission control (`Retry-After: 1`) или хранилище недоступно |
| `504` | `canceled` | Таймаут генерации (`http.generate-timeout`) или отмена клиента |
| `507` | `quota` | Превышена квота хранилища или лимит выходного файла |

Fallback-ответы и ошибки используют `Cache-Control` из `http.not-found-cache-control` (по умолчанию `no-store`).

## CORS

Deny-by-default: cross-origin ответы получают CORS-заголовки только для origin из `http.allowed-origins`. `OPTIONS` возвращает `204` c `Allow: GET, HEAD, OPTIONS`; при разрешённом origin отражаются `Access-Control-Allow-Origin` и (при `allow-credentials: true`) `Access-Control-Allow-Credentials`. Комбинация `"*"` + credentials запрещена конфигурацией.
