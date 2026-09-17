# Конфигурация

Все настройки задаются в YAML. CLI-флагов нет; допускается несколько
прикладных env-переменных (см. таблицы ниже и секцию `detection`).

Связанные документы: [ARCHITECTURE.md](ARCHITECTURE.md) (общая архитектура), [POLICIES.md](POLICIES.md) (секция `policy`), [FORMATS.md](FORMATS.md) (секции `encoders`, `video`), [AI.md](AI.md) (секция `detection`), [OBSERVABILITY.md](OBSERVABILITY.md) (секция `observability`), [API.md](API.md), [STORAGE.md](STORAGE.md), [DEPLOYMENT.md](DEPLOYMENT.md).

## Загрузка конфигурации

Прикладные env-переменные:

| Переменная | По умолчанию | Описание |
|------------|--------------|----------|
| `IMAGER_CONFIG_DIR` | `.` | Каталог с файлами конфигурации |
| `IMAGER_MODELS_DIR` | пусто | Каталог с ONNX-моделями: **и** каталог автоскачивания (entrypoint, см. `docker/entrypoint.sh`), **и** fallback для пустых `detection.face-model`/`object-model` (пути строятся как `<dir>/<имя_файла>`) |
| `IMAGER_MODEL_FACE_URL` | OpenCV Zoo | URL модели YuNet (лица) для автоскачивания (`docker/download-models.sh`); переопределение полезно для приватных зеркал |
| `IMAGER_MODEL_OBJECT_URL` | ONNX Model Zoo | URL модели SSD MobileNet v1 (объекты) для автоскачивания |
| `IMAGER_MODEL_SELFIE_URL` | пусто | URL тестового изображения `selfie.jpg`; скачивается только при задании (нужно лишь для тестов реального инференса, в prod не требуется) |
| `IMAGER_SKIP_MODELS` | `0` | `1` — полностью отключить автоскачивание моделей (offline-режим) |

Конфигурация разделена на **три слоя**, каждый из которых состоит из пары файлов «base + local»:

| Слой | Файлы | Назначение | Частота изменений |
|------|-------|-----------|-------------------|
| **setting** (фундамент) | `server.yaml` + `server-local.yaml` | Инфраструктура сервера: HTTP-порт/таймауты, пути хранения, подключения к стораджам, observability/logging, serve-original, безопасность, admin | Редко |
| **generate** (генерация) | `generate.yaml` + `generate-local.yaml` | Настройки генерации ассетов: пресеты, policy, форматы/энкодеры, ресайз, watermark, orientation, trim, color, detection | Часто |
| **failback** (резервы) | `failback.yaml` + `failback-local.yaml` | Резервные/необязательные fallback-механизмы: not-found, source-fallback | Почти никогда |

### Порядок загрузки и переопределения

1. **Внутри пары** выполняется deep merge `base ← local`:
   - вложенные map мержатся рекурсивно (ключи, не указанные в local, сохраняются);
   - скаляры заменяются значением из local;
   - списки заменяются **целиком** (дополнить список из local нельзя).
2. **Между слоями** три слитые map объединяются в фиксированном порядке `setting → generate → failback` (более специализированный слой выигрывает при конфликте скаляров). Если один и тот же **top-level ключ** встречается в нескольких базовых файлах — выполняется deep merge в этом порядке, а в лог пишется **warning** с перечнем конфликтующих файлов.
3. Результат строго декодируется в единую схему (`yaml.UnmarshalStrict` / `KnownFields(true)`): любой ключ вне схемы в любом из шести файлов — ошибка старта.
4. **Hot-reload конфигурации нет**: конфигурация читается один раз при старте; изменения требуют перезапуска. Единственное runtime-записывание — `generate-local.yaml` в learning-mode (см. [policy.learning-mode](#policylearning-mode)), оно применяется после перезапуска. Ватермарки дополнительно проверяются fail-fast при старте: путь из `watermarks.<имя>.path` должен существовать на диске, иначе старт завершается ошибкой.

**Обязательность файлов:**

- `server.yaml` — **обязателен**; отсутствие или невалидность останавливает старт.
- `server-local.yaml`, `generate.yaml`, `generate-local.yaml`, `failback.yaml`, `failback-local.yaml` — **опциональны**; их отсутствие — нормальная ситуация (значения берутся из умолчаний схемы или из `server.yaml`).

**Docker-контейнеры:** все базовые конфиги (`server.yaml`, `generate.yaml`, `failback.yaml`) и шаблоны `*-local.yaml.example` входят в образ ([`Dockerfile`](../Dockerfile): `setting/*.yaml` и `setting/*-local.yaml.example` → `/etc/imager/`). При старте контейнера entrypoint ([`docker/entrypoint.sh`](../docker/entrypoint.sh)):

1. сравнивает **релиз образа** (ENV `IMAGER_RELEASE`, задаваемый при сборке через `ARG IMAGER_RELEASE`; при пустом значении или `dev` — fallback: sha256-хеш содержимого дефолтных конфигов из `/etc/imager`, первые 16 символов) с маркером `$IMAGER_CONFIG_DIR/.imager-release`, записанным при предыдущем старте;
2. если маркера нет **или** релиз изменился (пересборка образа или pull обновлённого с Docker Hub `altrap/imager`) — **перезаписывает** базовые конфиги (`server.yaml`, `generate.yaml`, `failback.yaml`) версиями из `/etc/imager` (атомарно: временный файл + `mv`, права `root:imager 0640` как в Dockerfile);
3. если релиз тот же (обычный запуск/перезапуск) — копирует отсутствующие базовые конфиги и шаблоны из `/etc/imager` в каталог `IMAGER_CONFIG_DIR`, **только если целевой файл ещё не существует**;
4. для каждого шаблона `*-local.yaml.example` в `IMAGER_CONFIG_DIR` создаёт соответствующий `*-local.yaml`, также **только если целевой файл ещё не существует**.

Файлы `*-local.yaml` и `*-local.yaml.example` **никогда не перезаписываются** — клиентские правки (в том числе секреты) делайте **только** в них. Прямые правки `server.yaml` / `generate.yaml` / `failback.yaml` в смонтированном каталоге `./setting` будут **затёрты при обновлении образа**.

Механика маркера позволяет:

- запускать контейнер **без монтирования конфигов** — все дефолты из образа;
- монтировать **пустой** каталог `./setting` — дефолты подтянутся при первом старте;
- переопределять **только `*-local.yaml`** — базовые конфиги остаются из образа;
- переопределять **все конфиги целиком** — смонтировать свою папку с полным набором `server.yaml`/`generate.yaml`/`failback.yaml` (+ `*-local.yaml`); учтите, что при обновлении образа базовые файлы в этой папке будут перезаписаны дефолтами образа;
- получать **обновлённые дефолты** при переходе на новый образ без ручного удаления конфигов: достаточно, чтобы `IMAGER_RELEASE` нового образа отличался.

Если каталог конфигурации read-only (например, монтируется `:ro`), копирование пропускается с warning — это не фатально, `*-local.yaml` опциональны.

**Передача релиза при сборке** (CI): `docker build --build-arg IMAGER_RELEASE=$(git describe --tags --always) -t altrap/imager:<тег> .`. Если аргумент не передан (локальная сборка `make docker-build`), метка равна `dev` и entrypoint использует хеш дефолтных конфигов — force-sync сработает при любом изменении содержимого базовых конфигов в образе.

**Ключ `version`** (актуальна `"1"`): обязателен только в `server.yaml`. В `generate.yaml` / `failback.yaml` опционален; если присутствует — должен равняться `"1"`, иначе ошибка старта (защита от рассинхронизации версий слоёв).

Секреты (пароли, ключи S3/SFTP, `admin.token`) рекомендуется размещать в `*-local.yaml` (не коммитятся, см. `.gitignore`). Для S3 также доступны env `IMAGER_S3_ACCESS_KEY` / `IMAGER_S3_SECRET_KEY` (значение из YAML имеет приоритет).

### Переменные окружения (сводка)

| Переменная | Назначение |
|------------|-----------|
| `IMAGER_CONFIG_DIR` | Каталог конфигурации (см. таблицу выше); по умолчанию `.` |
| `IMAGER_MODELS_DIR` | Каталог ONNX-моделей (fallback для `detection.face-model`/`object-model`) |
| `IMAGER_MODEL_FACE_URL` / `IMAGER_MODEL_OBJECT_URL` / `IMAGER_MODEL_SELFIE_URL` / `IMAGER_SKIP_MODELS` | Автоскачивание моделей (см. таблицу выше) |
| `IMAGER_S3_ACCESS_KEY` / `IMAGER_S3_SECRET_KEY` | Секреты S3, если не заданы в YAML |
| `ONNXRUNTIME_SHARED_LIBRARY_PATH` | **Не используется**; путь к библиотеке задаётся только ключом `detection.onnx-runtime-lib` |

## Распределение секций по слоям

| Секция | Слой | Файл |
|--------|------|------|
| `version` | setting | `server.yaml` |
| `server` | setting | `server.yaml` |
| `http.allowed-origins`, `allow-credentials`, `cache-control`, `referrer-policy`, `csp`, `max-url-len`, `generate-timeout`, `max-concurrent-requests` | setting | `server.yaml` |
| `http.serve-original` | setting | `server.yaml` |
| `http.not-found`, `not-found-cache-control`, `source-fallback` | failback | `failback.yaml` |
| `source`, `result` | setting | `server.yaml` |
| `libvips.limits`, `libvips.operation-cache`, `libvips.metrics-interval` | setting | `server.yaml` |
| `encoders` (единая top-level секция кодирования) | setting | `server.yaml` |
| `shrink-on-load`, `color`, `watermark-cache`, `detection` (внутри `libvips`) | generate | `generate.yaml` |
| `metadata` | setting | `server.yaml` |
| `application.buffer-max-bytes` | setting | `server.yaml` |
| `application.singleflight-wait-timeout` | setting | `server.yaml` |
| `application.limits` | setting + generate | `server.yaml` (дефолт для всех слоёв) / `generate.yaml` (переопределение) |
| `observability` | setting | `server.yaml` |
| `admin` | setting | `server.yaml` |
| `policy` (presets, path-policies, learning-mode) | generate | `generate.yaml` |
| `watermarks` | generate | `generate.yaml` |
| `processing` | generate | `generate.yaml` |
| `detection` | generate | `generate.yaml` |

> Секция `http` — единственная, чьи подсекции расходятся по слоям: транспортные/security-ключи и `serve-original` живут в `server.yaml`, а fallback-подсекции (`not-found`, `not-found-cache-control`, `source-fallback`) — в `failback.yaml`. Благодаря deep merge подсекции одного top-level ключа из разных файлов корректно объединяются.

Полные самодокументированные примеры — [`setting/server.yaml`](../setting/server.yaml), [`setting/generate.yaml`](../setting/generate.yaml), [`setting/failback.yaml`](../setting/failback.yaml); локальные переопределения — [`setting/server-local.yaml`](../setting/server-local.yaml), [`setting/generate-local.yaml`](../setting/generate-local.yaml), [`setting/failback-local.yaml`](../setting/failback-local.yaml).

---

## server

| Ключ | Тип | По умолчанию | Описание |
|------|-----|--------------|----------|
| `addr` | string | `":8080"` | Адрес прослушивания TCP (`host:port`) |
| `read-header-timeout` | duration | `"5s"` | Таймаут чтения заголовков (защита от slowloris) |
| `read-timeout` | duration | `"15s"` | Таймаут чтения тела запроса |
| `write-timeout` | duration | `"60s"` | Таймаут записи ответа. По умолчанию вычисляется как `generate-timeout` + запас `30s` (см. ниже); если задан явно и `<= generate-timeout`, при старте логируется предупреждение о риске обрезанных ответов. Отрицательные значения таймаутов — ошибка старта |
| `idle-timeout` | duration | `"60s"` | Таймаут простоя keep-alive соединения |
| `shutdown-timeout` | duration | `"15s"` | Максимальное время graceful shutdown |
| `max-header-bytes` | int | `32768` | Максимум суммарного размера заголовков; превышение → `431` |
| `max-body-bytes` | int | `4096` | Лимит тела запроса (сервис тело не принимает); `0` = без лимита; отрицательное значение — ошибка старта |
| `metrics-auth.token` | string | пусто | Bearer-токен для доступа к `/metrics` (заголовок `X-Metrics-Token`); пусто = не требуется |
| `metrics-auth.allowed-ips` | list[string] | пусто | Список разрешённых IP/CIDR для `/metrics`; пусто = без IP-фильтра. Если задан хотя бы один из `token`/`allowed-ips` — `/metrics` защищён (403 без валидного токена/с неразрешённого IP) |

Duration — строка формата Go: `"5s"`, `"250ms"`, `"1m30s"`. Отрицательные значения запрещены.

## http

| Ключ | Тип | По умолчанию | Описание |
|------|-----|--------------|----------|
| `allowed-origins` | list[string] | пусто | CORS allowlist (`https://cdn.example.com`); пусто = CORS запрещён; `"*"` + `allow-credentials: true` — ошибка старта |
| `allow-credentials` | bool | `false` | Разрешать `Access-Control-Allow-Credentials` |
| `cache-control` | string | `"public, max-age=31536000, immutable"` | Cache-Control успешных канонических ассетов; пусто = не выставлять |
| `not-found-cache-control` | string | `"no-store"` | Cache-Control для 404/fallback-ответов |
| `referrer-policy` | string | `"no-referrer"` | Значение `Referrer-Policy` |
| `csp` | string | пусто | `Content-Security-Policy` для fallback-страниц |
| `max-url-len` | int | `1024` | Максимальная длина asset URL; превышение → `414` |
| `generate-timeout` | duration | `"30s"` | Таймаут генерации ассета; превышение → `504`. Должен быть строго меньше `server.write-timeout`: после генерации нужно время на передачу тела клиенту. Если `write-timeout` не задан, он вычисляется как `generate-timeout` + `30s`; если задан явно и `<= generate-timeout`, при старте логируется предупреждение |
| `max-concurrent-requests` | int | `0` | Admission control: максимум одновременных asset-запросов; превышение → `503` + динамический `Retry-After`; применяется только к asset-запросам, health/metrics доступны всегда. Если не задан (`0`), а задан `application.limits.concurrency` — лимит admission вычисляется из него (`limits.concurrency × 4`, см. ниже). **Bypass**: при переполнении семафора запрос, который может присоединиться к уже идущей singleflight-генерации того же ассета (тот же канонический URL), пропускается в обход семафора (вместо `503`) — join не создаёт новой работы, а лишь ожидает результат идущей генерации. **Динамический `Retry-After`** (admission): значение в секундах равно числу занятых слотов семафора (при переполнении — ёмкости), минимум `1`; не зависит от `http.retry-after` (тот применяется только к 503 при перегрузке процессора) |
| `retry-after` | duration | `"1s"` | Значение `Retry-After` для `503 overloaded` при перегрузке процессора (переполнение очереди слотов). `0`/пусто → дефолт `1s`; отрицательное — ошибка старта. Не влияет на динамический `Retry-After` admission-контроля (см. `max-concurrent-requests`) |

### http.not-found

Поведение при отсутствии ассета (мимо кэша и источника). Приоритет полей: `pixel` > `image` > `page` > `redirect`.

| Ключ | Тип | По умолчанию | Описание |
|------|-----|--------------|----------|
| `pixel` | bool | `false` | Отдавать прозрачный 1x1 пиксель в формате URL с кодом `404`. Применяется только если формат URL распознан и генератор пикселей доступен |
| `image` | string | пусто | Путь к статической картинке, отдаваемой с `404` |
| `page` | string | пусто | Путь к статическому HTML, отдаваемому с `404` |
| `redirect` | string | пусто | URL для `301` редиректа (для HEAD тело не пишется) |

Все fallback-ответы получают `Cache-Control` из `http.not-found-cache-control`. Если ни один fallback не настроен — корректный `404` с JSON error envelope. Ошибка чтения fallback-файла (отсутствует/недоступен) логируется и отдаётся обычный `404` (не 500).

Пример:

```yaml
http:
  not-found:
    pixel: true
    image: "example/not-found.png"
```

### http.source-fallback

Fallback на **исходный файл** при ошибке ассета, когда исходник существует. Применяется к ошибкам: неканонический URL, несуществующий пресет, недопустимый план, запрещённая политика. `OutcomeNotFound` (исходника нет) **не** покрывается — в этом случае применяется обычный not-found fallback.

| Ключ | Тип | По умолчанию | Описание |
|------|-----|--------------|----------|
| `enabled` | bool | `false` | Включать ли канонический source fallback (URL вида `name-format.ext`). |
| `status` | int | `404` | HTTP-статус ответа: `200` или `404` (0 → `404`); любое другое значение — ошибка старта. |
| `cache-control` | string | `"no-store"` | `Cache-Control` для source fallback-ответа. |

Когда fallback срабатывает, вместо пикселя/ошибки отдаётся исходный файл с его оригинальными `Content-Type`/`Content-Disposition`/`Cache-Control`/`ETag` (см. [API.md](API.md#source-fallback)).

**Назначение:** не дать CDN/браузерам закэшировать тысячи различных ошибочных ответов (несуществующие пресеты, неканонические URL, запрещённые политикой) — вместо них отдаётся один и тот же исходный файл с настраиваемым статусом.

Выбор статуса: `200` — ответ кэшируется как успешный и скрывает ошибки от клиентов и мониторинга; `404` (по умолчанию) — ответ кэшируется как ошибочный, мониторинг продолжает видеть ошибки. Для production рекомендуется `404`.

```yaml
http:
  source-fallback:
    enabled: false
    status: 404
    cache-control: "no-store"
```

### http.serve-original

**Отдельная фича** (не относится к `source-fallback`): отдача исходного файла по «простым» URL вида `/path/name.ext` со **статусом 200**.

Канонический URL ассета имеет форму `/{path}/{source_name}-{source_format}/{segment}@{dpr}.{out}`, где `segment` — имя пресета **или** custom-имя (размер-грамматика: `x`, `x200`, `200x`, `200x200`), а `@dpr` — необязательный суффикс плотности пикселей (без суффикса = 1; в URL явно допустимы только 2 и 3). URL без дефиса в имени исходника (например `/test/my.png`) не является валидным asset URL и по умолчанию даёт ошибку 400 `missing source format`. При `enabled: true` такие URL трактуются как прямой путь к исходнику: сервер проверяет путь теми же проверками безопасности (traversal, encoded-разделители, control-символы, canonicalization) и, если файл `test/my.png` существует в source-хранилище, отдаёт его со статусом `200` и заголовками `Content-Type`/`Content-Disposition`/`Cache-Control`/`ETag`. Если исходник не найден — применяется обычная обработка ошибки (400).

| Ключ | Тип | По умолчанию | Описание |
|------|-----|--------------|----------|
| `enabled` | bool | `false` | При `true` отдаёт исходный файл по «простым» URL `/path/name.ext` со статусом `200`. |
| `cache-control` | string | `"no-store"` | `Cache-Control` для ответа serve-original. |

Матрица поведения:

| `source-fallback.enabled` | `serve-original.enabled` | `/test/my.png` (простой URL) | `/test/my-png/200x200.webp` (канонический) |
|---------------------------|--------------------------|------------------------------|---------------------------------------------|
| `false`                   | `false` (дефолт)         | 400 `missing source format`  | 400 (обычная ошибка)                        |
| `false`                   | `true`                   | отдаётся исходник (200)      | 400 (обычная ошибка)                        |
| `true`                    | `false`                  | 400 `missing source format`  | отдаётся исходник (статус `status`)         |
| `true`                    | `true`                   | отдаётся исходник (200)      | отдаётся исходник (статус `status`)         |

```yaml
http:
  serve-original:
    enabled: false
    cache-control: "no-store"
```

## Лимиты и admission control

Сводка по всем лимитам запроса/генерации и их взаимодействию:

| Лимит | Ключ | Что ограничивает | При превышении |
|-------|------|------------------|----------------|
| Admission control | `http.max-concurrent-requests` | Одновременные asset-запросы (семафор) | `503` + динамический `Retry-After` (см. ниже) |
| Admission fallback | `application.limits.concurrency` | База для admission, если `max-concurrent-requests` не задан (`× 4`) | — |
| Перегрузка процессора | `libvips.limits.concurrency` | Слоты операций libvips | `503` + `http.retry-after` |
| Перегрузка детекции | `libvips.detection.*` | Слоты ONNX-инференса | **Деградация** к center-crop (не 503) |
| Размер исходника | `application.limits.source-bytes` | Байт исходника | Ошибка генерации (4xx/5xx) |
| Размер выхода | `application.limits.output-bytes` / `libvips.limits.output-bytes` | Байт результата | Прерывание генерации |
| Геометрия | `application.limits.width/height/pixels/dpr` | Размеры изображения | Ошибка генерации |
| Анимация | `application.limits.frames/duration` | Кадры/длительность | На application-уровне фактически не проверяются: кадры ограничивает `frames` пресета (лимит на этапе загрузки), `duration` движком пока не реализован — см. [PROCESSING.md](PROCESSING.md#анимации) |
| URL | `http.max-url-len` / `server.max-header-bytes` / `server.max-body-bytes` | Длина URL / заголовки / тело | `414` / `431` / `413` |

**Динамический `Retry-After` admission-контроля** (`adapters/httpapi/admission.go`): при переполнении семафора значение `Retry-After` (в секундах) равно числу занятых слотов (в ветке «переполнено» — ёмкости семафора), минимум `1`. Обоснование: чем больше запросов уже обрабатывается, тем дольше освобождение слота; фиксированный `Retry-After=1` не отражает реальную задержку. Это значение не связано с `http.retry-after` (тот применяется только к `503` при перегрузке процессора).

**Bypass admission**: при переполнении семафора запрос, который может присоединиться к уже идущей singleflight-генерации того же ассета (тот же канонический URL), пропускается в обход семафора без занятия слота — join не создаёт новой работы.

## policy

Deny-by-default политика. Всё запрещено, кроме явно разрешённого: запрос допускается, только если его сегмент (имя пресета или custom-имя) разрешён path-policy для пути запроса, а `@dpr` и выходной формат URL удовлетворяют настройкам пресета/custom. Подробности семантики — [SECURITY.md](SECURITY.md).

### policy.presets

**Map** именованных конфигураций обработки: ключ = имя пресета, значение = настройки. На пресеты ссылаются `path-policies[*].presets` по имени. Пресет становится доступным в URL только после включения его имени в какую-либо path-policy. Уникальность имён обеспечивается самим map; поиск пресета по имени — O(1).

| Поле | Тип | По умолчанию | Описание |
|------|-----|--------------|----------|
| *(ключ)* | string | обязателен | Имя пресета: ≤64 символа; допустимы буквы, цифры, `_`, `.`, `@`, `-`, `!`, `,`; уникально. Дефисы разрешены, но имя не должно выглядеть как «имя-формат» (`my-png`, `photo-jpg` — ошибка: правая часть последнего дефиса совпадает с медиа-расширением) или «префикс-размер» (`sc-120x80`, `crop-200x` — ошибка: правая часть выглядит как размер-грамматика); trailing-дефис (`img-`) — ошибка. Может содержать фиксированный суффикс `@2`/`@3` (например `"banner@2"`); суффиксы `@0`/`@1` запрещены. Если имя содержит `@N`, поле `dpr` ОБЯЗАНО быть задано и равно `N` (см. [правила dpr](#правила-dpr)) |
| `width` | uint32 | `0` | БАЗОВАЯ (логическая) ширина в px; `0` = не задана (вычисляется пропорционально). Итоговый размер = `width × dpr`. Максимум — 1 048 576 px (`asset.MaxDimension`); превышение — ошибка старта |
| `height` | uint32 | `0` | БАЗОВАЯ (логическая) высота в px; `0` = не задана (вычисляется пропорционально). Итоговый размер = `height × dpr`. Оба = `0` → исходный размер (`x`). Максимум — 1 048 576 px; превышение — ошибка старта |
| `output-formats` | list[string] | обязателен | **Массив** допустимых выходных форматов (whitelist): `jpeg\|png\|webp\|gif\|avif\|heif\|jxl`. Непустой; формат URL обязан входить в список. APNG-выход не поддерживается (запись требует libvips с libspng; APNG-вход при этом читается как анимированный PNG) |
| `dpr` | uint32 | ключ отсутствует | Множитель плотности пикселей. Имя с суффиксом `@N` — поле ОБЯЗАТЕЛЬНО и равно `N`. Имя без суффикса: не задан = wildcard-режим (`P.webp`, `P@2.webp`, `P@3.webp` допустимы в URL), `1` = фиксированный множитель (`@dpr` в URL запрещён), `2`/`3` = ошибка конфигурации. Допустимые значения поля: `0`–`3` (`0`/`1` = без умножения); больше `3` — ошибка старта. Подробнее — [правила dpr](#правила-dpr) |
| `crop` | string | `""` | `""`=resize, `center`=crop, `smart`=smart-crop, `face`=face-crop, `object`=object-crop, `face-fix`=face-fix-crop, `object-fix`=object-fix-crop. Fix-режимы: cover-масштаб до целевого размера без зума в лицо/объект — кроп только по пропорционально избыточной оси, позиция окна по центру области детекции (bbox + `detection.margin`) с clamp к границам; нет детекции — центр. Требуют настроенной детекции так же, как `face`/`object` |
| `trim` | bool | `false` | Обрезка однотонных полей. `crop`+`trim` — независимые фильтры: применяется сначала trim, затем кроп. |
| `quality` | uint32 | `0` | 0–100; 0 = `encoders.default-quality` (см. [encoders](#encoders)); для lossy-форматов передаётся кодеру как качество потери, для lossless (png/gif) управляет только усилием упаковки/палитрой |
| `frames` | uint32 | `0` | Максимум кадров анимации; 0 = без ограничения |
| `duration` | uint32 | `0` | Максимум длительности анимации (мс); 0 = без ограничения |
| `loop` | bool* | nil | nil = `processing.default-loop`; true = бесконечная анимация |
| `watermark` | string | пусто | Имя водяного знака из секции `watermarks`; неизвестное имя — ошибка старта |
| `auto-orient` | bool* | nil | Автоповорот по EXIF; nil = глобальный дефолт |
| `rotate` | string | `""` | `""`=наследовать дефолт, `none`=явно отключить, `90`/`180`/`270` |
| `flip` | string | `""` | `""`/`none`/`horizontal`/`vertical` |

Пример:

```yaml
presets:
  thumb:             # без @N: dpr: 1 = фиксированный множитель 1 (рекомендуется)
    width: 200
    height: 200
    output-formats: [webp, avif]
    quality: 85
    dpr: 1
  banner@2:          # @2 в имени: dpr ОБЯЗАН быть 2; width/height — базовые 1200×400 → итог 2400×800
    width: 1200
    height: 400
    output-formats: [webp, avif]
    dpr: 2
  portrait:
    crop: face
    trim: true
    width: 300
    height: 300
    output-formats: [jpeg]
    dpr: 1
```

#### Нативные ключи кодеков в пресетах

Помимо скалярного `quality`, пресет/custom может содержать **плоские нативные ключи** вида `<формат>-<параметр>` — имена параметров реестра `domain/encoding` с префиксом формата. Заданный ключ — **explicit override**: он отменяет автомаппинг от quality (и переопределяет глобальное значение секции `encoders`) ровно для этого параметра. Дополнительно поддерживаются APNG-ключи `apng-compression-level` и `apng-interlace` (APNG-выход при этом не поддерживается движком — см. [output-formats](#policypresets)).

| Ключ | Формат | Тип/диапазон | Авто* | Описание |
|------|--------|--------------|-------|----------|
| `jpeg-quality` | jpeg | int `[1,100]` | — | Per-format quality (lossy), переопределяет скалярный `quality` для jpeg |
| `jpeg-progressive` | jpeg | bool | — | Прогрессивный (interlaced) JPEG; `false` = baseline |
| `webp-quality` | webp | int `[1,100]` | — | Per-format quality (lossy) |
| `webp-reduction-effort` | webp | int `[0,6]` | ✅ | Reduction effort WebP: больше = лучше сжатие, медленнее |
| `webp-lossless` | webp | bool | — | Lossless-режим WebP |
| `webp-near-lossless` | webp | bool | — | Near-lossless WebP |
| `avif-quality` | avif | int `[1,100]` | — | Per-format quality (lossy) |
| `avif-speed` | avif | int `[0,9]` | ✅ | Speed AVIF (инверсия: меньше = медленнее, лучше сжатие); `0` валиден |
| `avif-lossless` | avif | bool | — | Lossless-режим AVIF |
| `heif-quality` | heif | int `[1,100]` | — | Per-format quality (lossy) |
| `jxl-quality` | jxl | int `[1,100]` | — | Per-format quality (lossy) |
| `jxl-effort` | jxl | int `[3,9]` | ✅ | Effort JPEG XL: больше = лучше сжатие, медленнее |
| `jxl-lossless` | jxl | bool | — | Lossless-режим JPEG XL |
| `png-compression-level` | png | int `[1,9]` | ✅ | Уровень сжатия PNG |
| `png-interlace` | png | bool | — | Чересстрочный (Adam7) PNG |
| `png-palette` | png | bool | ✅ | Палитровый (quantized) экспорт |
| `png-palette-colors` | png | int `[2,256]` | ✅ | Максимум цветов палитры |
| `png-palette-bit-depth` | png | int `[1,8]` | ✅ | Битность палитры (снап к 1/2/4/8) |
| `png-dither` | png | float `[0,1]` | — | Дизеринг палитрового PNG (дефолт 1.0) |
| `gif-effort` | gif | int `[1,10]` | ✅ | Effort GIF (дефолт libvips 7) |
| `gif-bit-depth` | gif | int `[1,8]` | — | Битность палитры GIF (дефолт 8) |
| `gif-dither` | gif | float `[0,1]` | — | Дизеринг палитры GIF (дефолт 1.0) |

\* **Авто** — параметр участвует в якорном автомаппинге от `quality`, когда он **не задан** ни в пресете, ни глобально в `encoders` (см. [encoders](#encoders)).

Семантика:

- **Explicit override отменяет автомаппинг**: заданный нативный ключ применяется как есть (с валидацией диапазона при старте — fail-fast); якорная формула от quality для этого параметра не вызывается.
- **Per-format quality** (`webp-quality` и т.п.) — только для **lossy-форматов** (jpeg/webp/avif/heif/jxl): переопределяет скалярный `quality` именно для этого формата. Для **lossless-форматов** (png/gif) задание quality-ключа формата (`png-quality` и т.п.) — **ошибка конфигурации**: их упаковка управляется только скалярным `quality`.
- **Скалярный `quality`** (0–100): применяется к выходному качеству для всех lossy-форматов пресета; для lossless-форматов потерь не вводит — влияет только на усилие упаковки (compression-level/effort) и палитровую автоматику.
- Все ключи сверяются с реестром `domain/encoding`: неизвестный ключ, ключ чужого формата или значение вне диапазона — ошибка старта.

### policy.path-policies

Политики по префиксам пути ассет-URL. **Map**: ключ = префикс пути, значение = настройки пути. Выбор policy — longest-prefix match (совпадение по сегментам: путь равен префиксу или начинается с `префикс/`); `"/"` — fallback для всех путей без более специфичного совпадения. deny-by-default: если для пути нет подходящей path-policy (нет `"/"` и совпадений) — запрос отклоняется. Дубликаты после нормализации (`"a/b"` и `"/a/b/"` → `"/a/b"`) — ошибка старта.

| Поле | Тип | Описание |
|------|-----|----------|
| *(ключ)* | string | Префикс пути, нормализуется в `/prefix` (например `"basket/products"` → `"/basket/products"`); `"/"` — fallback |
| `presets` | list[string] | Список имён глобальных пресетов (`policy.presets`), доступных на этом пути; неизвестное имя — ошибка старта |
| `customs` | map | Custom-настройки пути: ключ = custom-имя, значение = настройки как у пресетов (см. [customs](#policypath-policiescustoms)) |

### policy.path-policies.*.customs

Custom-настройки пути: быстрый способ разрешить произвольные размеры без объявления пресета. **Имя** имеет размер-грамматику `x` (оригинал) / `x200` (только высота) / `200x` (только ширина) / `200x200` (точный размер), опционально с суффиксом `@2`/`@3` (например `200x100@2`). Настройки — те же, что у пресетов (`output-formats` обязателен, `crop`, `quality`, `dpr` и т.д.), включая плоские нативные ключи кодирования (`webp-quality` и т.п.).

Разрешение сегмента URL (приоритет): 1) точное совпадение полного имени `segment@dpr` в customs; 2) базовое имя в customs (wildcard-dpr: `@dpr` в URL разрешён только если `dpr` не задан); 3) тот же алгоритм в presets пути (customs имеют приоритет); 4) ничего не найдено → deny (`segment_not_allowed`). Конфликт `@dpr` URL с фиксированным `@N` в имени — deny (`dpr_not_allowed`).

- **Размер из имени, width/height опциональны**: размер custom берётся из имени (`200x200`); `width`/`height` в настройках задавать не обязательно — они уже в URL/имени. Если заданы, переопределяют соответствующие стороны (вторая берётся из имени).
- **Wildcard-@dpr**: имя custom без суффикса `@N` и `dpr` не задан — `@dpr` из URL свободный (`200x200.webp`, `200x200@2.webp`, `200x200@3.webp`). Если поле `dpr` задано — только `dpr: 1` (значения `2`/`3` без `@N` — ошибка конфигурации). Суффикс `@N` в имени custom (например `200x100@2`) фиксирует dpr: поле `dpr` ОБЯЗАТЕЛЬНО и должно быть РАВНО `N`; в URL допустим только тот же `@dpr`.
- **Приоритет при разрешении**: customs имеют приоритет над presets пути.

```yaml
path-policies:
  "/":
    presets: ["thumb"]
    customs:
      x:
        crop: center
        output-formats: [webp]
      200x100@2:
        output-formats: [webp, avif]
        dpr: 2
  "/banners":
    presets: ["banner", "banner@2"]
    customs:
      200x100@2:
        output-formats: [webp, avif]
        dpr: 2
```

### policy.learning-mode

**Learning-mode** — режим «обучения» политики: сервер генерирует и отдаёт ассеты, которые не разрешены текущими `path-policies`, но **не сохраняет** их в result-хранилище. Наблюдаемые URL автоматически накапливаются в `generate-local.yaml` (слой локальных переопределений).

| Поле | Тип | По умолчанию | Описание |
|------|-----|--------------|----------|
| `learning-mode` | bool | `false` | Включить learning-mode при старте. Изменяется только вручную (правка конфига + перезапуск сервиса); runtime-переключателей нет |

> **Автосброс при остановке**: при штатной остановке сервера (graceful shutdown по SIGINT/SIGTERM) learning-mode автоматически выключается — в `generate-local.yaml` записывается `learning-mode: false`, и после перезапуска сервер работает в обычном режиме. Режим включается заново только вручную (правка конфига + перезапуск).

Поведение при включённом learning-mode:

- **Bypass admission**: запрос, не подходящий ни под одну path-policy, генерируется и отдаётся клиенту, если его сегмент — размер-грамматика (`120x60`, `x200`, `200x`, `x`). Сегмент-имя несуществующего пресета (например `banner`) остаётся `403` — learning-mode не «угадывает» пресеты. Исключение: если имя сегмента совпадает с одним из объявленных в конфигурации пресетов (`policy.presets`), запрос также генерируется (настройки пресета применяются), а наблюдение пополняет `presets` path-policy.
- **Ничего не сохраняется**: даже ассеты, разрешённые path-policy, не публикуются в result-хранилище, пока режим включён. Уже сохранённые ассеты отдаются из кэша как обычно.
- **Автонакопление path-policies**: каждый обслуженный запрос с размер-сегментом наблюдается и записывается в `generate-local.yaml` (дебаунс ~2 с + финальная запись при graceful shutdown; буфер наблюдений — 256, при переполнении наблюдение отбрасывается). Запись comment-preserving: существующие комментарии и структура файла сохраняются, новые пути помечаются комментарием `# added by learning-mode`; повторные форматы того же размера ДОБАВЛЯЮТСЯ в `output-formats` существующего custom. Формат записи — `path-policies` с custom-размером и выходным форматом, например:

```yaml
policy:
  learning-mode: true
  path-policies:
    # added by learning-mode
    /banners:
      customs:
        120x60:
          output-formats: [webp]
```

- **Требования**: для записи `generate-local.yaml` нужен каталог конфигурации (`IMAGER_CONFIG_DIR`); без него runtime-флаг работает, но наблюдения не сохраняются (Recorder не создаётся). При `learning-mode: false` Recorder не создаётся вовсе — `generate-local.yaml` не читается и не пишется.
- **Сброс при shutdown**: при graceful shutdown в `generate-local.yaml` записывается `learning-mode: false` (comment-preserving; если файла нет — создаётся минимальный документ `version: "1"` / `policy: {learning-mode: false}`), наблюдения сохраняются.
- **Рекомендация**: после переноса накопленных правил в `generate.yaml` выключите learning-mode (`learning-mode: false` в конфиге + перезапуск сервиса) — сервер вернётся к deny-by-default и начнёт сохранять ассеты.

### Правила dpr

Поведение `@dpr` в URL зависит от того, задан ли `dpr` в настройках пресета/custom и содержит ли его имя фиксированный `@dpr`-суффикс. Здесь `P` — базовое имя сегмента (пресет или custom без `@dpr`).

**Пресеты (`policy.presets`):**

| Настройки пресета | Допустимые URL | Требования конфигурации |
|-------------------|----------------|------------------------|
| `dpr` **не задан** (ключ отсутствует), имя без `@N` | `P.webp`, `P@2.webp`, `P@3.webp` | Wildcard-режим: `@dpr` в URL свободный, без суффикса = 1. Рекомендуется прописывать `dpr: 1` явно |
| `dpr: 1`, имя без `@N` | только `P.webp` (без суффикса) | `@dpr` в URL запрещён; множитель = 1. Значения `2`/`3` без `@N` в имени — ошибка конфигурации (для `@dpr`-вариантов заводите отдельные пресеты `P@2`/`P@3`) |
| имя содержит `@N` (`banner@2`), `dpr: N` | только тот же `@dpr` в URL (`banner@2.webp`) | Поле `dpr` ОБЯЗАНО присутствовать и быть РАВНО `N`; иначе (нет `dpr` или другое значение) — ошибка конфигурации |

**Customs (`path-policies.*.customs`):** правила те же, что для пресетов:

- Имя custom без `@N`, `dpr` не задан — wildcard-режим: `P.webp`, `P@2.webp`, `P@3.webp` допустимы (без суффикса = 1; рекомендуется прописывать `dpr: 1` явно).
- Имя custom без `@N`, `dpr` задан — только `dpr: 1` (фиксированный множитель 1, `@dpr` в URL запрещён); `dpr: 2`/`3` без `@N` — ошибка конфигурации (для `@dpr`-вариантов заводите отдельные customs `P@2`/`P@3`).
- Имя custom с `@N` (`200x100@2`) — поле `dpr` ОБЯЗАТЕЛЬНО и должно быть РАВНО `N`. Отсутствие — ошибка «dpr is required for @2 suffix (set dpr: 2)»; другое значение (`dpr: 3`) — ошибка «dpr 3 conflicts with dpr 2 in name (must be equal)». В URL допустим только тот же `@dpr`.

**Общие правила:**

- Суффиксы `@0`/`@1` **запрещены всегда** (и в имени пресета/custom, и в URL).
- В URL явно допустимы только `@2` и `@3` (без суффикса = `@1`).
- Если имя содержит `@2`, а URL — другой `@dpr` (`banner@3.webp`) — запрос отклоняется (`dpr_not_allowed`).
- **width/height пресета — БАЗОВЫЕ (логические) значения**; итоговый размер = `width×dpr` и `height×dpr` вычисляется внутри сервиса. Для `200x200@2` пишите `width: 200, height: 200, dpr: 2` → на выходе 400×400. Писать `width: 400` для `@2` НЕ нужно (иначе итог будет 800×800).
- **width/height кастомных (URL-заданных) пресетов опциональны**: размер custom берётся из имени (`200x200`), поэтому в настройках их можно не указывать; если указаны — переопределяют соответствующие стороны (поле dpr при этом учитывает `@N` из имени так же, как для пресетов).

## watermarks

**Map** именованных деклараций ватермарок: ключ = имя ватермарки, значение = настройки. Уникальность имён обеспечивается самим map; поиск по имени — O(1). Секция опциональна.

| Поле | Тип | По умолчанию | Описание |
|------|-----|--------------|----------|
| *(ключ)* | string | обязателен | Уникальное имя для ссылок |
| `path` | string | обязателен | Путь к PNG-файлу на диске; отсутствие файла — ошибка старта |
| `position` | string | `center` | `top\|bottom\|left\|right\|center`. Одиночное ключевое слово: вторая ось — центр (CSS-подобно). Неизвестное значение — ошибка старта |
| `repeat` | string | `no-repeat` | `no-repeat\|repeat\|repeat-x\|repeat-y\|round\|space` |
| `size` | string | пусто = исходный размер | `contain` — вписать в холст с сохранением пропорций; `cover` — покрыть холст (излишек обрезается по `position`); пусто — исходный (натуральный) размер знака; `"{w}px {h}px"` — фиксированный размер; `"{w}px"` — ширина фикс, высота пропорциональна аспекту знака; `"{n}%"` — ширина копии = `n`% ширины холста, высота пропорциональна аспекту знака (пропорции сохраняются); `"{n}"` — то же, что `"{n}%"` (число без суффикса = процент). Проценты — в `[1,100]`, px — положительные; вне диапазона — ошибка старта |
| `opacity` | int | `100` | Прозрачность водяного знака в процентах: `100` — непрозрачный, `0` — полностью прозрачный (невидимый). Значение вне диапазона [0,100] — **ошибка старта** (fail-fast, см. `composition/watermark_config_test.go`); валидация выполняется в `config.Validate`, а `NormalizeWatermarkOpacity` (fallback на 100) применяется только к значениям, не заданным через конфиг |

Ограничения движков: libvips поддерживает position/repeat/size полностью, включая покадровое наложение на анимированные выходы (GIF/WebP/HEIF/APNG/AVIF) с сохранением delay/loop. Все копии repeat/tile-раскладки накладываются одним composite-вызовом.

Приоритет применения водяного знака: пресет/custom (по имени из `policy.presets.<name>.watermark` / `policy.path-policies.*.customs.*.watermark`) → `processing.default-watermark`. Ватермарка разрешается в спецификацию при компиляции конфигурации: неизвестное имя в пресете/custom или в `default-watermark` — ошибка старта.

```yaml
watermarks:
  logo:
    path: "/etc/imager/watermarks/logo.png"
    position: bottom         # допустимые значения: top|bottom|left|right|center
    repeat: no-repeat
    size: contain            # contain | cover | "" (исходный размер) | "200px 50px" | "200px" | "50%" | "50"
    opacity: 100             # 0-100; 100 = непрозрачный, 0 = невидимый
```

Поведение `size`:

- `contain` — копия масштабируется с сохранением пропорций так, чтобы целиком поместиться в холст;
- `cover` — копия масштабируется так, чтобы покрыть весь холст; излишек обрезается **по позиции** (`position`): при `center` излишек срезается равномерно с обеих сторон, при `left`/`right`/`top`/`bottom` — с противоположного края;
- пустое значение — копия накладывается в **исходном** (натуральном) размере файла знака;
- `"{w}px {h}px"` — фиксированный размер в пикселях;
- `"{w}px"` — фиксированная ширина, высота вычисляется пропорционально аспекту файла знака;
- `"{n}%"` (и `"{n}"`) — ширина и высота копии равны `n`% соответствующих измерений холста (аспект знака игнорируется); `n` в `[1,100]`.

При `dpr > 1` холст масштабируется на `dpr`, поэтому фиксированные (`px`) и натуральные размеры копии также умножаются на `dpr` — ватермарка выглядит одинаково при любом `dpr`. `contain`/`cover`/проценты зависят от холста и масштабируются автоматически.

## processing

| Ключ | Тип | По умолчанию | Описание |
|------|-----|--------------|----------|
| `default-loop` | bool* | `true` | Зацикливание анимаций GIF/WebP/HEIF/AVIF по умолчанию |
| `default-watermark` | string | пусто | Водяной знак по умолчанию (имя из `watermarks`) |
| `default-auto-orient` | bool* | `true` | Автоповорот по EXIF Orientation |
| `default-rotate` | string | `""` | Фиксированный поворот: `""`/`none`/`90`/`180`/`270` |
| `default-flip` | string | `""` | Отражение: `""`/`none`/`horizontal`/`vertical` |
| `default-trim-mode` | string | `"auto"` | Определение цвета фона для trim: `auto` (по краевому пикселю) или `color` |
| `default-trim-color` | string | пусто | Цвет фона trim в hex (`"#ffffff"`), только при `default-trim-mode: color` |
| `default-trim-tolerance` | float | `0` | Допуск сравнения пикселей с фоном `[0,1]` |
| `default-resize-background` | string | пусто | Цвет фона letterbox/pillarbox при resize с обоими заданными измерениями (hex `"#RRGGBB"`). Пусто = прозрачность, где возможна; для форматов без альфы (JPEG) при пустом значении — белый `"#ffffff"`. Невалидный hex — ошибка старта |
| `default-video-frame-percent` | int `[0,100]` | `50` | Процент от длительности видео, на котором выбирается кадр превью; `0` = кадр с начала. Вне диапазона [0,100] — ошибка старта |
| `default-video-min-contrast` | float `[0,1]` | `0.05` | Минимальная контрастность кадра; ниже — кадр считается неудачным и пропускается; `0` = проверка отключена. Вне диапазона [0,1] — ошибка старта |
| `default-video-frame-step` | int (≥1) | `1` | Шаг вперёд (в кадрах) при неудачной проверке контрастности. Значение < 1 — ошибка старта |
| `default-video-attempts` | int (≥1) | `3` | Общее число попыток поиска подходящего кадра. Значение < 1 — ошибка старта. Если ни один кадр не прошёл проверку контрастности, отдаётся последний извлечённый кадр |

Порядок применения ориентации: auto-orient → rotate → flip, затем resize/crop/trim.

**Видео-источники** (`mp4`, `webm`, `mov`, `mkv`, `avi`, `m4v`, `mpg`, `mpeg`, `wmv`, `flv`, `3gp`, `ogv`, `ts`, `mts`, `m2ts` — контейнеры, декодируемые ffmpeg): ассет строится из **одного кадра** видео, извлечённого через ffmpeg (видео процессоры не декодируют). Кадр сохраняется в result как `<видео-ключ>/x.jpg` и фиксируется в метаданных — следующие запросы используют сохранённый кадр, видео не открывается. При недостаточной контрастности кадра (`default-video-min-contrast`) поиск идёт с шагом `default-video-frame-step`, максимум `default-video-attempts` попыток.

**output-formats: auto** — в списке `output-formats` пресета/custom допустим элемент `auto` (или пустая строка `""`): он разрешает ТОЛЬКО формат исходника запроса (для видео — `jpg`, т.к. ассет строится из извлечённого JPEG-кадра). Может соседствовать с явными форматами: `output-formats: [auto, webp]`. Passthrough оригинала видео (`x.mp4`) при `auto` отклоняется — укажите формат явно: `output-formats: [auto, mp4]`.

## encoders

Единая **top-level** секция настроек кодирования. Живёт в `server.yaml`; переопределения возможны через `*-local.yaml` и более специализированные слои (deep merge).

Структура: `default-quality` + именованные группы форматов (`jpeg`, `webp`, `avif`, `heif`, `jxl`, `png`, `gif`) с нативными параметрами реестра `domain/encoding`. Эффективные параметры **каждого экспорта** разрешаются через `domain/encoding.Resolve` на каждый экспорт по строгому приоритету:

> **preset override (плоские нативные ключи) > `encoders` YAML > якорный автомаппинг от quality > реестровый дефолт**

Качество экспорта (для lossy-форматов):

> **per-format quality пресета > `encoders.<формат>.quality` > скалярный `quality` пресета/запроса (`0` = `encoders.default-quality`)**

### encoders.default-quality

| Ключ | Тип | По умолчанию | Описание |
|------|-----|--------------|----------|
| `default-quality` | int `[1,100]` | `80` (дефолт кода при `0`; в примере `server.yaml` — `85`) | Качество сжатия по умолчанию, когда `quality` не задан в пресете/запросе. Fallback для **lossy**-форматов (передаётся кодеру как качество потери) и **якорь автомаппинга** для lossless-форматов (усилие упаковки/палитра — потерь не вводит). Вне `[1,100]` — ошибка старта |

Секция `encoders` также содержит группу `apng` (`compression-level` `[1,9]`, дефолт 6; `interlace`, дефолт false) — параметры валидируются по реестру, но APNG-выход движком не поддерживается (см. [output-formats](#policypresets)).

### Таблица per-format ключей

| Ключ | Диапазон | Дефолт | Авто* | Описание |
|------|----------|--------|-------|----------|
| `jpeg.progressive` | bool | `false` | — | Прогрессивный (interlaced) JPEG; `false` = baseline |
| `webp.quality` | int `[1,100]` | `null` | — | Глобальный дефолт качества WebP; `null` = из запроса/`default-quality` |
| `webp.reduction-effort` | int `[0,6]` | `4` | ✅ | Reduction effort WebP: больше = лучше сжатие, медленнее. Якорь q75→4 |
| `webp.lossless` | bool | `false` | — | Lossless-режим WebP |
| `webp.near-lossless` | bool | `false` | — | Near-lossless WebP |
| `avif.quality` | int `[1,100]` | `null` | — | Глобальный дефолт качества AVIF |
| `avif.speed` | int `[0,9]` | `6` | ✅ | Speed AVIF (инверсия: меньше = медленнее, лучше сжатие); `0` валиден. Якорь q80→6 |
| `avif.lossless` | bool | `false` | — | Lossless-режим AVIF |
| `heif.quality` | int `[1,100]` | `null` | — | Глобальный дефолт качества HEIF/HEIC |
| `jxl.quality` | int `[1,100]` | `null` | — | Глобальный дефолт качества JPEG XL |
| `jxl.effort` | int `[3,9]` | `7` | ✅ | Effort JPEG XL. Якорь q75→7. `0` невалиден |
| `jxl.lossless` | bool | `false` | — | Lossless-режим JPEG XL |
| `png.quality` | int `[1,100]` | `null` | — | Якорное качество PNG: влияет только на упаковку/палитру, потерь не вводит |
| `png.compression-level` | int `[1,9]` | `6` | ✅ | Уровень сжатия PNG. Якорь q85→6 |
| `png.interlace` | bool | `false` | — | Чересстрочный (Adam7) PNG |
| `png.palette` | bool | `false` | ✅ | Палитровый (quantized) экспорт: автоматика q<90→ON, q≥90→OFF (truecolor) |
| `png.palette-colors` | int `[2,256]` | `256` | ✅ | Максимум цветов палитры. Якорь q85→256 |
| `png.palette-bit-depth` | int `[1,8]` | `8` | ✅ | Битность палитры (снап к 1/2/4/8), из числа цветов |
| `png.dither` | float `[0,1]` | `1.0` | — | Дизеринг палитрового PNG (значим при palette=true) |
| `gif.effort` | int `[1,10]` | `7` (libvips) | ✅ | Effort GIF. Якорь q75→7 |
| `gif.bit-depth` | int `[1,8]` | `8` | — | Битность палитры GIF |
| `gif.dither` | float `[0,1]` | `1.0` | — | Дизеринг палитры GIF |

\* **Авто** — параметр участвует в якорном автомаппинге от `quality`, когда он не задан ни в пресете (override), ни глобально в `encoders`. Остальные параметры — физические переключатели (bool) или константы-дефолты, от качества не зависят.

Все значения валидируются по реестру `domain/encoding` при старте (fail-fast): невалидный диапазон или неизвестный для формата ключ — ошибка конфигурации. `null`/отсутствие ключа = «не задано глобально». Для bool-параметров диапазон реестра не проверяется (проверяется только принадлежность параметра формату).

### Якорные формулы автомаппинга

| Параметр | Диапазон | Якорь ↦ значение | Поведение |
|----------|----------|------------------|-----------|
| webp `reduction-effort` | `[0,6]` | q75→4, q100→6, q0→0 | линейно, монотонно |
| avif `speed` | `[0,9]` | q80→6, q100→0, q0→9 | **инверсия**: меньше число = медленнее, лучше сжатие |
| jxl `effort` | `[3,9]` | q75→7, q100→9, q0→3 | линейно, монотонно |
| gif `effort` | `[1,10]` | q75→7, q100→10, q0→1 | линейно, монотонно |
| png `compression-level` | `[1,9]` | q85→6, q100→9, q0→1 | кусочно-линейно: `q≤85`: `1+round(5q/85)`; `q>85`: `6+round((q-85)/5)` |
| png `palette` | bool | q<90 → ON, q≥90 → OFF | при высоком качестве палитра выключается (защита градиентов) |
| png `palette-colors` | `[2,256]` | q85→256, q0→2 | `clamp(round(2+(q/85)·254), 2, 256)` |
| png `palette-bit-depth` | `[1,8]` | colors=256→8 | из `palette-colors`: ≤2→1, ≤4→2, ≤16→4, иначе 8 |

Пример полной секции (совпадает с [`setting/server.yaml`](../setting/server.yaml)):

```yaml
encoders:
  default-quality: 85
  jpeg:
    progressive: true
  webp:
    quality: null            # null = из запроса / encoders.default-quality
    reduction-effort: 4
    lossless: false
    near-lossless: false
  avif:
    quality: null
    speed: 6
    lossless: false
  heif:
    quality: null
  jxl:
    quality: null
    effort: 7
    lossless: false
  png:
    quality: null
    compression-level: 6
    interlace: false
    palette: false
    palette-colors: 256
    palette-bit-depth: 8
    dither: null
  gif:
    effort: 7
    bit-depth: 8
    dither: null
```

## source / result

Хранилища исходников и результатов настраиваются независимо. Тип задаётся ключом `storage`.

Типы `fs`, `s3`, `sftp`, `ftp`, `ftps` доступны для source и result; `http` — только для source (ошибка старта для result). Детальное описание всех типов и параметров хранилищ — [STORAGE.md](STORAGE.md).

Обязательные поля по типам (fail-fast при старте):

- `s3` — `bucket` обязателен; `access-key`/`secret-key` задаются только парой (или через env `IMAGER_S3_ACCESS_KEY`/`IMAGER_S3_SECRET_KEY`; значение из YAML имеет приоритет);
- `sftp` — `addr` + `user` + `host-key-fingerprint` (SHA256:...) обязательны; пароль или `private-key-file`;
- `ftp`/`ftps` — `addr` обязателен; для `ftps` `tls-verify: false` запрещён (ошибка старта);
- `http` — `base-url` обязателен; для HTTP source при незаданном `spool-max-bytes` применяется безопасный дефолт 512 MiB (0 = неограниченный буфер — DoS-риск).

Параметры соединения (`dial-timeout`, `read-timeout`, `max-attempts`, `max-idle-conns`, `max-idle-conns-per-host`, `idle-conn-timeout`, `max-conns`, `metadata-ttl`) применяются к remote-хранилищам; дефолты: dial `30s`, read `60s`, attempts `3`, idle-conns `100`, idle-conns-per-host `10`, idle-conn-timeout `90s`, max-conns (SFTP/FTP/FTPS) `2`, metadata-ttl (S3) `30s` (0 = кэш метаданных отключён).

## libvips

Основной движок (govips, in-process). Секция актуальна для сборок с `-tags libvips`.

| Ключ (`libvips.limits.*`) | Тип | По умолчанию | Описание |
|---------------------------|-----|--------------|----------|
| `timeout` | duration | `"30s"` | Context deadline одной операции обработки |
| `source-bytes` | int | `10485760` (10 MiB) | Лимит размера входных данных (байт). Должен быть ≥ `application.limits.source-bytes`, иначе крупный исходник пройдёт application-лимит, но будет отсечён процессором |
| `output-bytes` | int | — | Лимит размера выходных данных (bounded writer) |
| `concurrency` | int | `16` (в коде при 0) | Максимум одновременных операций libvips |
| `threads` | int | `1` | Число потоков libvips (`vips_concurrency_set`) |
| `max-cache-mem` | int | 50 MiB | Максимум памяти кэша libvips |
| `max-cache-files` | int | default govips (0) | Максимум открытых файлов кэша |
| `max-cache-size` | int | `100` | Максимум операций в кэше |

Отрицательные значения всех лимитов запрещены (fail-fast): в govips значение `< 0` означает «default govips», что делает конфигурацию непредсказуемой.

Параметры кодировщиков задаются в единой top-level секции [encoders](#encoders). Эффективные параметры каждого экспорта разрешаются через `domain/encoding.Resolve`: **preset override > `encoders` YAML > автомаппинг от quality > реестровый дефолт**. Диапазоны валидируются при старте по реестру `domain/encoding`: невалидное значение — ошибка конфигурации (fail-fast), не runtime-ошибка.

DPI-нормализация: при экспорте `xres`/`yres` сбрасываются к 72 DPI (после `stripAllMetadata`), чтобы просмотрщики не масштабировали изображение по DPI-метаданным исходника (например 300 DPI из сканера). Изображения уже с 72 DPI не перекопируются (быстрый путь). Константа не конфигурируется.

Shrink-on-load (`libvips.shrink-on-load.*`) — предварительное уменьшение при декодировании JPEG/WebP/GIF/HEIF/AVIF. Коэффициент вычисляется из целевого размера плана с запасом ×2 (после shrink размер гарантированно ≥ цели; точный resize выполняется далее как обычно). Решение консервативно: shrink НЕ применяется при trim/smart-crop/face-crop/object-crop, ручной ориентации или ненейтральном EXIF-повороте, `size=x`, неизвестных размерах исходника и для анимированных GIF. Для JPEG применяется shrink степени двойки (1/2, 1/4, 1/8), для WebP/HEIF/AVIF/GIF — scale-on-load.

| Ключ (`libvips.shrink-on-load.*`) | Тип | По умолчанию | Описание |
|:------------------------------------|------|--------------|----------|
| `enabled` | bool | `true` | Включить shrink-on-load при декодировании |

ICC color management (`libvips.color.mode`) — политика обработки embedded-ICC-профиля исходника. Без color management цвета CMYK/ProPhoto/Display-P3 исходников искажаются (профиль удаляется). Режимы:

- `strip` (дефолт): профиль удаляется при обработке (`stripAllMetadata`).
- `transform`: embedded-профиль конвертируется в стандартный sRGB через PCS (govips `TransformICCProfile` → `vips_icc_transform` с профилем sRGB IEC61966-2.1) ПЕРЕД пиксельной обработкой; после конверсии изображение обрабатывается как обычное sRGB. **Fast-path (нулевой оверхед)**: sRGB-совместимый профиль (проверка по сигнатуре/имени без lcms-конверсии) или изображение уже в sRGB colorspace без профиля — конверсия не выполняется. **Отказоустойчивость**: битый/отсутствующий профиль или ошибка lcms не роняют запрос — fallback на strip-поведение с warning-логом.
- `keep`: embedded-профиль сохраняется в выходном изображении (профиль не удаляется при экспорте).

| Ключ (`libvips.color.*`) | Тип | По умолчанию | Описание |
|:---------------------------|-----|--------------|----------|
| `mode` | string | `"strip"` | Режим: `strip` \| `transform` \| `keep` (fail-fast: неизвестное значение — ошибка старта) |

Passthrough fast-path: если план обработки ничего не меняет (целевой формат совпадает с исходным, размер уже целевой или `size=x`, нет watermark/trim/ориентации/детекции/ограничений кадров) — исходные байты возвращаются как есть, без decode/encode. В режиме `transform` passthrough дополнительно допускается для исходников с sRGB-совместимым ICC-профилем (конверсия была бы no-op); в режиме `keep` — для любого профиля (профиль сохраняется в выходе). EXIF/XMP/IPTC и прочие метаданные по-прежнему блокируют passthrough.

Operation cache libvips (`libvips.operation-cache.enabled`) — управление кэшем результатов операций libvips (`vips_cache`). Кэш полезен для повторяющихся операций на одних и тех же изображениях, но для stateless-обработчика он **бесполезен, ест память и несёт риск на musl/Alpine** — рекомендуемое значение для продакшена: `false`. При отключении в Startup передаются нулевые лимиты кэша (`vips_cache_set_max_mem(0)` / `vips_cache_set_max(0)` / `vips_cache_set_max_files(0)`): в govips значение `0` означает **полное отключение** кэша (не «без лимита»; значение `< 0` = default govips).

| Ключ (`libvips.operation-cache.*`) | Тип | По умолчанию | Описание |
|:-------------------------------------|-----|--------------|----------|
| `enabled` | bool | `true` | Включить operation cache; `false` = кэш отключён (нулевые лимиты при Startup) |

```yaml
libvips:
  limits:
    timeout: "30s"
    output-bytes: 10485760
    concurrency: 2
    threads: 4
    max-cache-mem: 52428800
  shrink-on-load:
    enabled: true
  color:
    mode: strip
  operation-cache:
    enabled: false
  watermark-cache:
    enabled: true
    max-files: 32
    max-bytes: 67108864
    ttl: "5m"
  detection:
    concurrency: 0
    max-wait: "5s"
  metrics-interval: "15s"
```

Кэш файлов водяных знаков (`libvips.watermark-cache.*`) — in-memory кэш исходных БАЙТОВ файлов водяных знаков, keyed по пути файла (не по конфигурации наложения). Инвалидация — по mtime/размеру файла плюс TTL; вытеснение — LRU по числу записей и бюджету байтов; дедупликация параллельной загрузки — singleflight. При промахе кэша выполняется прозрачное чтение с диска.

| Ключ (`libvips.watermark-cache.*`) | Тип | По умолчанию | Описание |
|:------------------------------------|-----|--------------|----------|
| `enabled` | bool | `true` | Включить кэш; `false` = каждое использование читает диск |
| `max-files` | int | `32` | Максимум записей (файлов) в кэше (`0` = дефолт; отрицательное — ошибка старта) |
| `max-bytes` | int | 64 MiB | Суммарный бюджет памяти кэша в байтах; файл больше бюджета не кэшируется (`0` = дефолт; отрицательное — ошибка старта) |
| `ttl` | duration | `"5m"` | Страховочное время жизни записи (`0` = дефолт; отрицательное — ошибка старта) |

Detection-семафор (`libvips.detection.*`) — отдельный bounded-семафор для тяжёлых CPU-bound ONNX-инференсов (face-crop/object-crop). Схема handoff: на время инференса libvips-слот освобождается и берётся detection-слот, после инференса слоты меняются обратно. Это защищает лёгкие операции (decode/resize/encode) от голодания при потоке fc/oc-запросов, сохраняя ограничение суммарной конкурентности (защита от OOM). Порядок захвата строго детерминирован (detection-слот берётся только при удержании libvips-слота), вложенного удержания обоих слотов во время долгих операций нет — дедлок невозможен.

**Graceful degradation при перегрузке**: при переполнении очереди ожидания или истечении `max-wait` (detection-семафора) запрос НЕ завершается ошибкой 503, а ДЕГРАДИРУЕТ: обработка продолжается с fallback-фокусировкой (center-crop / без smart-focus), libvips-слот остаётся у запроса (перекладывание не происходит). Событие логируется (warning) и считается счётчиком `imager_detection_degraded_total` (через `/metrics`). Ошибки самой ONNX-модели (сбой инференса) деградацией НЕ покрываются — они остаются ошибками обработки.

| Ключ (`libvips.detection.*`) | Тип | По умолчанию | Описание |
|:-------------------------------|-----|--------------|----------|
| `concurrency` | int | `max(1, GOMAXPROCS/2)` | Максимум одновременных ONNX-инференсов (`0` = дефолт) |
| `max-wait` | duration | `"5s"` | Бюджет ожидания detection-слота; истечение → деградация к center-crop (`0` = дефолт; отрицательное — ошибка старта) |

Vips-метрики (`libvips.metrics-interval`) — периодический сбор метрик libvips и кэша ватермарок в observability; экспортируются через `/metrics` как gauge-и:

- `imager_vips_tracked_memory_bytes` — tracked memory libvips;
- `imager_vips_tracked_allocs` — число активных аллокаций;
- `imager_vips_open_files` — открытые файлы libvips;
- `imager_vips_mem_highwater_bytes` — пик tracked memory;
- `imager_vips_operations_total` — суммарное число операций govips;
- `imager_vips_watermark_cache_hits_total` / `imager_vips_watermark_cache_misses_total` / `imager_vips_watermark_cache_entries` / `imager_vips_watermark_cache_bytes` — метрики кэша водяных знаков.

Метрики асинхронной публикации (экспортируются через `/metrics`):

- `imager_publish_queue_depth` — текущая глубина bounded-очереди фоновой публикации (gauge). Рост при стабильной нагрузке указывает на то, что воркеры не успевают писать в remote; переполнение очереди включает синхронный fallback (публикация на пути ответа).
- `imager_publish_errors_total` — счётчик ошибок фоновой публикации после исчерпания retry/backoff. Результат с ошибкой НЕ попадает в кэш и будет сгенерирован повторно; длительный рост — признак проблем с result-хранилищем.

Публикация результата в кэш выполняется фоновыми воркерами (подробнее — в [PROCESSING.md](PROCESSING.md), раздел «Асинхронная публикация»). Размер очереди/число воркеров задаются в параметрах построения composition-root (не через YAML) и по умолчанию равны 512 и 4; graceful drain при shutdown — с таймаутом 5 с. При переполнении очереди публикация выполняется синхронно (fallback): результат не теряется. Ошибка публикации после исчерпания retry не влияет на уже отправленный клиенту ответ.

Сбор отказоустойчив: паника/ошибка провайдера не влияет на обработку запросов (значения просто не обновляются до следующего тика); goroutine-сборщик останавливается при graceful shutdown.

| Ключ (`libvips.*`) | Тип | По умолчанию | Описание |
|:--------------------|:-----|--------------|----------|
| `metrics-interval` | duration | `"15s"` | Интервал сбора vips-метрик (минимум `"1s"`; `0` = дефолт; отрицательное — ошибка старта) |

Примечания по производительности libvips-адаптера:

- **Лимит кадров анимации** (`frames` в пресетах/лимитах) применяется на этапе загрузки (`NumPages`), что дешевле пост-обрезки стека кадров.
- **Sequential access** выставляется при загрузке для операций с одним линейным проходом по пикселям (resize/crop/smart-crop без trim).
- **Premultiply**: перед resize изображений с альфа-каналом (PNG/WebP/GIF с прозрачностью) выполняется Premultiply → resize → Unpremultiply — исключает тёмные ореолы на полупрозрачных краях. Для анимаций операция применяется ко всему стеку кадров с сохранением delay/loop.

## detection

Детектор лиц/объектов для операций `face`/`object` (face-crop/object-crop) и `face-fix`/`object-fix`. Требует сборки с `-tags onnx`. Включение операции задаётся путём к модели: пустой путь = операция отключена.

Путь к модели можно задать двумя способами (приоритет у первого):
1. явно в YAML (`face-model` / `object-model`);
2. через env `IMAGER_MODELS_DIR` (каталог) — если ключ в YAML пуст, путь строится как `<IMAGER_MODELS_DIR>/face_detection_yunet_2023mar.onnx` (и `/ssd_mobilenet_v1_12.onnx`). В Docker (compose) задаётся `/etc/imager/models`, **куда монтируется хостовый `./models` в режиме rw**, а модели скачиваются автоматически при старте контейнера (`docker/entrypoint.sh` → `docker/download-models.sh`). Ручное размещение файлов не требуется; при недоступном источнике контейнер запускается с предупреждением, а операции `fc`/`oc` остаются отключёнными (пустые пути). См. [DEPLOYMENT.md](DEPLOYMENT.md).

| Ключ | Тип | По умолчанию | Описание |
|------|-----|--------------|----------|
| `face-model` | string | пусто | Путь к ONNX-модели YuNet (лица). Пусто = face-crop отключён (запрос с `face`/`face-fix` вернёт понятную ошибку) |
| `object-model` | string | пусто | Путь к ONNX-модели SSD/YOLO (объекты). Пусто = object-crop отключён |
| `onnx-runtime-lib` | string | пусто | Путь к библиотеке ONNX Runtime (libonnxruntime). Пусто = кроссплатформенная автодетекция (Linux `.so` / Windows `.dll` / macOS `.dylib`) по стандартным путям. Задаётся ТОЛЬКО через конфиг; env `ONNXRUNTIME_SHARED_LIBRARY_PATH` не используется |
| `confidence-threshold` | float | `0.5` | Порог уверенности детекции `[0,1]`; боксы с уверенностью ниже порога отбрасываются до NMS. Вне диапазона — ошибка старта |
| `max-objects` | int | `5` | Максимум объектов после NMS (первые N самых уверенных). Должен быть > 0; `≤ 0` — ошибка старта |
| `margin` | float | `0.1` | Отступ вокруг найденной области как доля её размера `[0,1]`; применяется равномерно по обеим осям (половина с каждой стороны). `0` = кроп строго по bounding box. Отрицательное значение — ошибка старта |

Модели загружаются лениво при первом запросе и кэшируются в памяти до завершения процесса. Отсутствие файла модели на диске — ошибка при первом обращении (не при старте), чтобы сервис продолжал работать без ONNX-инфраструктуры. Путь к библиотеке ONNX Runtime (`onnx-runtime-lib`) берётся из конфиг-файла, а не из env-переменной `ONNXRUNTIME_SHARED_LIBRARY_PATH`.

Автодетект библиотеки (`onnx-runtime-lib` пуст) кроссплатформенный и работает на Linux, Windows и macOS:

- **Linux** — ищутся `libonnxruntime.so`, версионированные `libonnxruntime.so.*` (glob, любая минорная версия — по убыванию) и `onnxruntime.so` в `/usr/lib/`, `/usr/lib/x86_64-linux-gnu/`, `/usr/local/lib/`, `/opt/onnxruntime/lib/`;
- **Windows** — ищется `onnxruntime.dll` (дефолт биндинга через `LoadLibrary`), рядом с исполняемым файлом, в `%WINDIR%\System32` и в каталоге установки ONNX Runtime (`%ProgramFiles%\onnxruntime\lib\`);
- **macOS** — ищутся `libonnxruntime.dylib`, версионированные `libonnxruntime.*.dylib` (glob, любая минорная версия) в `/usr/local/lib/`, `/opt/homebrew/lib/`, `/opt/onnxruntime/lib/`, а также голое имя `libonnxruntime.dylib`.

Приоритет: путь из конфига → первый существующий кандидат автодетекта → дефолт биндинга. Если ни один файл не найден, биндинг `github.com/yalue/onnxruntime_go` сам пробует `onnxruntime.so` (Linux/macOS) или `onnxruntime.dll` (Windows) через системные механизмы (ld.so / dyld / LoadLibrary).

Fallback: если указанный в конфиге путь не существует как файл, он автоматически заменяется результатом автодетекта (WARN-сообщение в лог); при пустом значении или сработавшем fallback успешная автодетекция пишется в лог как INFO с найденным путём. Путь из конфига, который существует, но не загружается биндингом, НЕ заменяется автоматически — биндинг вернёт ошибку загрузки по этому пути (её причина видна в сообщении `detection: init onnxruntime: ...`).

Версионированные имена (`libonnxruntime.so.*`, `libonnxruntime.*.dylib`) ищутся через glob-паттерны и НЕ привязаны к конкретной минорной версии: обновление ONNX Runtime не требует правок кода или конфигов. При успешной автодетекции рекомендуется задать `onnx-runtime-lib` явно, чтобы сообщение INFO больше не появлялось.

## metadata

Sidecar-кэш результатов ИИ-детекции (лица/объекты) и `largest_ai_asset`: каждая модель вызывается один раз на родительский файл.

| Ключ | Тип | По умолчанию | Описание |
|------|-----|--------------|----------|
| `enabled` | bool | `true` | Включить sidecar-кэш |
| `dir` | string | `<result-каталог>` | Явный локальный путь метаданных; рекомендуется задавать при remote-result. Пусто = дефолт `<эффективный локальный result-каталог>` (без подкаталога `.meta`) |

Метаданные всегда хранятся локально, независимо от типов source/result.

> **Пакетные удаления и S3.** Если для результата используется S3-хранилище, `metadata.dir` лучше указывать **локально** (на локальном диске), а не в S3. Тогда пакетное удаление ассетов в S3 не затрагивает sidecar-метаданные — они остаются на локальном диске и не удаляются вместе с объектами S3.

## application

Прикладные лимиты генерации ассетов. `0` в любом поле = без ограничения.

| Ключ | Тип | По умолчанию | Описание |
|------|-----|--------------|----------|
| `limits.source-bytes` | int64 | `0` | Максимум размера исходного файла (байт) |
| `limits.output-bytes` | int64 | `0` | Максимум размера выходного файла (байт); превышение прерывает генерацию |
| `limits.width` | uint32 | `0` | Максимальная ширина (px) |
| `limits.height` | uint32 | `0` | Максимальная высота (px) |
| `limits.pixels` | int64 | `0` | Максимум пикселей (width×height) |
| `limits.dpr` | uint32 | `0` | Максимальный DPR запроса (1/2/3) |
| `limits.frames` | uint32 | `0` | Максимум кадров анимации |
| `limits.duration` | uint32 | `0` | Максимум длительности анимации (мс) |
| `limits.concurrency` | uint32 | `0` | Максимум одновременных генераций от одного клиента (проверяется до/после обработки). Если `http.max-concurrent-requests` не задан, используется как база для admission-лимита (`limits.concurrency × 4`). Отрицательные значения всех лимитов — ошибка старта (fail-fast) |
| `buffer-max-bytes` | int64 | `524288000` (500 MiB) | Бюджет памяти spillable-буфера (source+result); при исчерпании — спул на диск |
| `singleflight-wait-timeout` | duration | `"60s"` | Таймаут ожидания завершения владельца keyed singleflight (защита от «зависшего» владельца генерации). Истечение → waiter получает `503` + `Retry-After` (временная недоступность). `0` = отключено (вечное ожидание до отмены контекста запроса). Дефолт `60s` = 2×`http.generate-timeout` (`30s`), чтобы не обрывать легитимных waiter'ов. Отрицательное значение — ошибка старта |


## observability

| Ключ | Тип | По умолчанию | Описание |
|------|-----|--------------|----------|
| `log-level` | string | `"info"` | `debug\|info\|warn\|error` (регистронезависимо). Неизвестное значение маппится в `info` (fail-safe) |

### observability.asset-errors

Observability ошибок asset URL (неканонический URL, несуществующий пресет, недопустимый план, запрещённая политика). Включает счётчики, bounded top-paths и структурные логи.

| Ключ | Тип | По умолчанию | Описание |
|------|-----|--------------|----------|
| `enabled` | bool | `true` | Включать ли учёт ошибок asset URL (счётчики, top-paths, структурные логи). Дефолт `true` задаётся в коде (composition/runtimeconfig.go); в примере `server.yaml` указан явно. `log-level` валидируется при старте: допустимы `debug\|info\|warn\|error` (пусто = `warn`) |
| `log-level` | string | `"warn"` | Уровень структурного лога ошибки: `debug\|info\|warn\|error` |

При `enabled: true` ошибки фиксируются:

- **счётчиком** `imager_asset_errors` в `/metrics` — по категории `kind` (`parse` | `preset_not_found` | `invalid_plan` | `policy_denied`);
- **структурными логами** с полями `kind`, `url`, `preset`, `reason` на уровне `log-level`;
- **top-paths** — при включённом `top-paths` (см. ниже).

#### observability.asset-errors.top-paths

Bounded LRU-реестр проблемных путей с вытеснением. Кардинальность ограничена `max-entries`, поэтому не раздувает память даже при большом числе уникальных ошибочных URL.

| Ключ | Тип | По умолчанию | Описание |
|------|-----|--------------|----------|
| `enabled` | bool | `false` | Включать ли учёт top-paths |
| `max-entries` | int | `1024` | Максимальное число отслеживаемых путей (LRU; при превышении вытесняется наименее недавно использованный) |
| `report-top` | int | `20` | Число путей в отчёте (Top(n)) |
| `key-mode` | string | `"source"` | Режим ключа: `source` — путь исходника (если извлечь не удалось — raw URL); `hash` — sha256 первых 16 байт hex URL (не публикует пути исходников в метриках). Неизвестное значение — ошибка старта; `max-entries`/`report-top` отрицательные — ошибка старта |

Отчёт топ-`report-top` путей доступен в `/metrics`.

```yaml
observability:
  asset-errors:
    enabled: true
    log-level: warn
    top-paths:
      enabled: false
      max-entries: 1024
      report-top: 20
      key-mode: source   # source | hash
```

## admin

Административные эндпоинты для управления ассетами: фоновая генерация всех/выбранных ассетов исходника и удаление ассетов. При включении обязателен непустой `token`, иначе старт завершится ошибкой (fail-fast).

Маршруты (регистрируются в mux только при `enabled: true`):

- `POST /admin/assets/generate` — генерация ассетов;
- `DELETE /admin/assets/delete` — удаление ассетов.

Авторизация — заголовок `Authorization: Bearer <token>` (constant-time сравнение через `crypto/subtle`). Неверный/отсутствующий токен → `403`. Подробности API — [API.md](API.md#админ-эндпоинты).

| Ключ | Тип | По умолчанию | Описание |
|------|-----|--------------|----------|
| `enabled` | bool | `false` | Включать ли admin-эндпоинты. |
| `token` | string | `""` | Bearer-токен для авторизации. Обязателен при `enabled: true` (иначе fail-fast). |
| `workers` | int | `2` | Число параллельных фоновых генераций (≥ 1; `0` = дефолт 2; отрицательное — ошибка старта). |
| `queue-size` | int | `64` | Ёмкость очереди задач; переполнение → HTTP `503` (`0` = дефолт 64; отрицательное — ошибка старта). |
| `wait-timeout` | duration | `"300s"` | Таймаут режима `wait=true` (ожидания завершения всех ассетов до ответа); превышение → HTTP `504` (`0` = дефолт 300s; отрицательное — ошибка старта). |

**Безопасность токена:** `token` — секрет. Храните его в `server-local.yaml` (не коммитится, см. `.gitignore`) или в секрет-менеджере; не логируйте и не включайте в URL. Рекомендации — [SECURITY.md](SECURITY.md#админ-эндпоинты).

```yaml
admin:
  enabled: false      # по умолчанию выключено
  token: ""           # обязательный при enabled=true (иначе fail-fast)
  workers: 2
  queue-size: 64
  wait-timeout: "300s"
```

## Примеры конфигурации по слоям

Каждая фича целиком живёт в своём файле-слое. Ниже — минимальный рабочий набор из трёх файлов.

### `server.yaml` (фундамент)

```yaml
version: "1"

server:
  addr: ":8080"

http:
  allowed-origins:
    - "https://cdn.example.com"
  cache-control: "public, max-age=2592000"
  max-concurrent-requests: 32

source:
  storage: s3
  bucket: "my-images-source"
  prefix: "source/"
  endpoint: "https://storage.yandexcloud.net"
  region: "ru-central1"
  access-key: "AKIA..."        # или env IMAGER_S3_ACCESS_KEY
  secret-key: "..."            # или env IMAGER_S3_SECRET_KEY

result:
  storage: s3
  bucket: "my-images-result"
  prefix: "gen/"

metadata:
  dir: "./data/meta"

libvips:
  limits:
    concurrency: 4
    threads: 4
    timeout: "30s"
    output-bytes: 10485760
  operation-cache:
    enabled: false
  metrics-interval: "15s"

encoders:
  default-quality: 85
  webp:
    reduction-effort: 4
  avif:
    speed: 6
  png:
    compression-level: 6

application:
  limits:
    source-bytes: 10485760
    output-bytes: 10485760
    width: 2000
    height: 2000
    pixels: 4000000
    dpr: 3
    frames: 150
    duration: 60000
    concurrency: 10
  buffer-max-bytes: 524288000
  singleflight-wait-timeout: "60s"

observability:
  log-level: "warn"
```

### `generate.yaml` (генерация ассетов)

```yaml
policy:
  presets:
    thumb:
      width: 200
      height: 200
      output-formats: [webp, avif]
      quality: 85
      dpr: 1
    thumb@2:
      width: 200          # базовые 200×200 × dpr 2 = 400×400 на выходе
      height: 200
      output-formats: [webp, avif]
      quality: 85
      dpr: 2
    banner@2:
      width: 1200         # базовые 1200×400 × dpr 2 = 2400×800 на выходе
      height: 400
      output-formats: [webp, avif]
      dpr: 2
  path-policies:
    "/":
      presets: ["thumb", "thumb@2"]
      customs:
        x:
          output-formats: [webp]
        200x200:
          output-formats: [webp]
        x200@2:
          output-formats: [webp, avif]
          dpr: 2
    "/banners":
      presets: ["banner@2"]
      customs:
        200x100@2:
          output-formats: [webp, avif]
          dpr: 2

libvips:
  shrink-on-load:
    enabled: true
  color:
    mode: strip
  watermark-cache:
    enabled: true
    max-files: 32
    max-bytes: 67108864
    ttl: "5m"
  detection:
    concurrency: 0
    max-wait: "5s"

detection:
  face-model: ""
  object-model: ""

application:
  limits:
    source-bytes: 10485760
    output-bytes: 10485760
```

### `failback.yaml` (резервные механизмы)

```yaml
http:
  not-found-cache-control: "no-store"
  not-found:
    pixel: true
  source-fallback:
    enabled: false
    status: 404
    cache-control: "no-store"
  serve-original:
    enabled: false
    cache-control: "no-store"
```

### Локальные переопределения

Секреты и локальные отклонения от базовых файлов размещайте в `*-local.yaml`. Например, `server-local.yaml`:

```yaml
server:
  addr: ":9090"

source:
  storage: s3
  bucket: "my-images-source"
  prefix: "source/"
  endpoint: "https://storage.yandexcloud.net"
  region: "ru-central1"
  access-key: "AKIA..."        # или env IMAGER_S3_ACCESS_KEY
  secret-key: "..."            # или env IMAGER_S3_SECRET_KEY

metadata:
  dir: "./data/meta"

observability:
  log-level: "warn"
