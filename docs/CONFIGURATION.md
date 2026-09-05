# Конфигурация

Все настройки задаются в YAML. Прикладных env-переменных и CLI-флагов нет.

## Загрузка конфигурации

Единственная переменная окружения:

| Переменная | По умолчанию | Описание |
|------------|--------------|----------|
| `IMAGER_CONFIG_DIR` | `.` | Каталог с файлами конфигурации |

Конфигурация разделена на **три слоя**, каждый из которых состоит из пары файлов «base + local»:

| Слой | Файлы | Назначение | Частота изменений |
|------|-------|-----------|-------------------|
| **setting** (фундамент) | `setting.yaml` + `setting-local.yaml` | Инфраструктура сервера: HTTP-порт/таймауты, пути хранения, подключения к стораджам, observability/logging, serve-original, безопасность, admin | Редко |
| **generate** (генерация) | `generate.yaml` + `generate-local.yaml` | Настройки генерации ассетов: пресеты, policy, форматы/энкодеры, ресайз, watermark, orientation, trim, color, detection | Часто |
| **failback** (резервы) | `failback.yaml` + `failback-local.yaml` | Резервные/необязательные fallback-механизмы: not-found, source-fallback | Почти никогда |

### Порядок загрузки и переопределения

1. **Внутри пары** выполняется deep merge `base ← local`:
   - вложенные map мержатся рекурсивно (ключи, не указанные в local, сохраняются);
   - скаляры заменяются значением из local;
   - списки заменяются **целиком** (дополнить список из local нельзя).
2. **Между слоями** три слитые map объединяются в фиксированном порядке `setting → generate → failback` (более специализированный слой выигрывает при конфликте скаляров). Если один и тот же **top-level ключ** встречается в нескольких базовых файлах — выполняется deep merge в этом порядке, а в лог пишется **warning** с перечнем конфликтующих файлов.
3. Результат строго декодируется в единую схему (`yaml.UnmarshalStrict`): любой ключ вне схемы в любом из шести файлов — ошибка старта.

**Обязательность файлов:**

- `setting.yaml` — **обязателен**; отсутствие или невалидность останавливает старт.
- `setting-local.yaml`, `generate.yaml`, `generate-local.yaml`, `failback.yaml`, `failback-local.yaml` — **опциональны**; их отсутствие — нормальная ситуация (значения берутся из умолчаний схемы или из `setting.yaml`).

**Ключ `version`** (актуальна `"1"`): обязателен только в `setting.yaml`. В `generate.yaml` / `failback.yaml` опционален; если присутствует — должен равняться `"1"`, иначе ошибка старта (защита от рассинхронизации версий слоёв).

Секреты (пароли, ключи S3/SFTP, `admin.token`) рекомендуется размещать в `*-local.yaml` (не коммитятся, см. `.gitignore`). Для S3 также доступны env `IMAGER_S3_ACCESS_KEY` / `IMAGER_S3_SECRET_KEY`.

## Распределение секций по слоям

| Секция | Слой | Файл |
|--------|------|------|
| `version` | setting | `setting.yaml` |
| `server` | setting | `setting.yaml` |
| `http.allowed-origins`, `allow-credentials`, `cache-control`, `referrer-policy`, `csp`, `max-url-len`, `generate-timeout`, `max-concurrent-requests` | setting | `setting.yaml` |
| `http.serve-original` | setting | `setting.yaml` |
| `http.not-found`, `not-found-cache-control`, `source-fallback` | failback | `failback.yaml` |
| `source`, `result` | setting | `setting.yaml` |
| `libvips.limits`, `libvips.operation-cache`, `libvips.metrics-interval` | setting | `setting.yaml` |
| `libvips.encoders`, `shrink-on-load`, `color`, `watermark-cache`, `detection` | generate | `generate.yaml` |
| `metadata` | setting | `setting.yaml` |
| `application.buffer-max-bytes` | setting | `setting.yaml` |
| `application.limits` | setting + generate | `setting.yaml` (дефолт для всех слоёв) / `generate.yaml` (переопределение) |
| `observability` | setting | `setting.yaml` |
| `admin` | setting | `setting.yaml` |
| `policy` (presets, path-policies, learning-mode) | generate | `generate.yaml` |
| `watermarks` | generate | `generate.yaml` |
| `processing` | generate | `generate.yaml` |
| `detection` | generate | `generate.yaml` |

> Секция `http` — единственная, чьи подсекции расходятся по слоям: транспортные/security-ключи и `serve-original` живут в `setting.yaml`, а fallback-подсекции (`not-found`, `not-found-cache-control`, `source-fallback`) — в `failback.yaml`. Благодаря deep merge подсекции одного top-level ключа из разных файлов корректно объединяются.

Полные самодокументированные примеры — [`config/setting.yaml`](../config/setting.yaml), [`config/generate.yaml`](../config/generate.yaml), [`config/failback.yaml`](../config/failback.yaml); локальные переопределения — [`config/setting-local.yaml`](../config/setting-local.yaml), [`config/generate-local.yaml`](../config/generate-local.yaml), [`config/failback-local.yaml`](../config/failback-local.yaml).

## Обратная совместимость

Старый монолитный `setting.yaml`, содержащий все секции (включая «переехавшие» в generate/failback), продолжает работать: merge выполняется на уровне map до strict-декодирования, а схема едина. Если новый `generate.yaml`/`failback.yaml` дублирует секцию из старого `setting.yaml` — применяется deep merge в порядке `setting → generate → failback` (значение из более специализированного слоя побеждает) с warning в лог.

> **Исключение — новая policy-грамматика.** Старые ключи `policy.global` (`authorization`, `allowed-presets`, `size-rules`, `limits`), `policy.presets[].size` (вместо `width`/`height`), строковый `output-formats` и slice-формат `presets`/`watermarks` (список с полем `name`; вместо него — map, где имя = ключ) больше **не поддерживаются**: strict-декодирование отклоняет их как неизвестные поля. При миграции перепишите policy-секцию по новому формату (см. [policy](#policy)) и перенесите лимиты в `application.limits`.

---

## server

| Ключ | Тип | По умолчанию | Описание |
|------|-----|--------------|----------|
| `addr` | string | `":8080"` | Адрес прослушивания TCP (`host:port`) |
| `read-header-timeout` | duration | `"5s"` | Таймаут чтения заголовков (защита от slowloris) |
| `read-timeout` | duration | `"15s"` | Таймаут чтения тела запроса |
| `write-timeout` | duration | `"30s"` | Таймаут записи ответа |
| `idle-timeout` | duration | `"60s"` | Таймаут простоя keep-alive соединения |
| `shutdown-timeout` | duration | `"15s"` | Максимальное время graceful shutdown |
| `max-header-bytes` | int | `32768` | Максимум суммарного размера заголовков; превышение → `431` |
| `max-body-bytes` | int | `4096` | Лимит тела запроса (сервис тело не принимает); `0` = без лимита |

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
| `generate-timeout` | duration | `"30s"` | Таймаут генерации ассета; превышение → `504` |
| `max-concurrent-requests` | int | `0` | Admission control: максимум одновременных asset-запросов; превышение → `503` + `Retry-After: 1`; применяется только к asset-запросам, health/metrics доступны всегда |

### http.not-found

Поведение при отсутствии ассета (мимо кэша и источника). Приоритет полей: `pixel` > `redirect` > `image` > `page`.

| Ключ | Тип | По умолчанию | Описание |
|------|-----|--------------|----------|
| `pixel` | bool | `false` | Отдавать прозрачный 1x1 пиксель в формате URL с кодом `404` |
| `image` | string | пусто | Путь к статической картинке, отдаваемой с `404` |
| `page` | string | пусто | Путь к статическому HTML, отдаваемому с `404` |
| `redirect` | string | пусто | URL для `301` редиректа |

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
| `enabled` | bool | `false` | Включать ли канонический source fallback (URL вида `name-format.ext`). Выключен по умолчанию (текущее поведение). |
| `status` | int | `404` | HTTP-статус ответа: `200` или `404` (0 → `404`). |
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

Канонический URL ассета имеет форму `/{path}/{source_name}-{source_format}/{segment}@{dpr}.{out}`, где `segment` — имя пресета **или** custom-имя (размер-грамматика: `x`, `x200`, `200x`, `200x200`), а `@dpr` — необязательный суффикс плотности пикселей (без суффикса = 1; в URL явно допустимы только 2 и 3). Transform-коды в URL отсутствуют: операция (resize/crop/smart-crop/face-crop/object-crop) определяется **только** полем `crop` пресета/custom. URL без дефиса в имени исходника (например `/test/my.png`) не является валидным asset URL и по умолчанию даёт ошибку 400 `missing source format`. При `enabled: true` такие URL трактуются как прямой путь к исходнику: сервер проверяет путь теми же проверками безопасности (traversal, encoded-разделители, control-символы, canonicalization) и, если файл `test/my.png` существует в source-хранилище, отдаёт его со статусом `200` и заголовками `Content-Type`/`Content-Disposition`/`Cache-Control`/`ETag`. Если исходник не найден — применяется обычная обработка ошибки (400).

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

## policy

Deny-by-default политика. Всё запрещено, кроме явно разрешённого: запрос допускается, только если его сегмент (имя пресета или custom-имя) разрешён path-policy для пути запроса, а `@dpr` и выходной формат URL удовлетворяют настройкам пресета/custom. Подробности семантики — [SECURITY.md](SECURITY.md).

### policy.presets

**Map** именованных конфигураций обработки: ключ = имя пресета, значение = настройки. На пресеты ссылаются `path-policies[*].presets` по имени. Пресет становится доступным в URL только после включения его имени в какую-либо path-policy. Уникальность имён обеспечивается самим map; поиск пресета по имени — O(1).

| Поле | Тип | По умолчанию | Описание |
|------|-----|--------------|----------|
| *(ключ)* | string | обязателен | Имя пресета: ≤64 символа, без дефисов; допустимы буквы, цифры, `_`, `.`, `@`; уникально. Может содержать фиксированный суффикс `@2`/`@3` (например `"banner@2"`); суффиксы `@0`/`@1` запрещены. Если имя содержит `@N`, поле `dpr` ОБЯЗАНО быть задано и равно `N` (см. [правила dpr](#правила-dpr)) |
| `width` | uint32 | `0` | БАЗОВАЯ (логическая) ширина в px; `0` = не задана (вычисляется пропорционально). Итоговый размер = `width × dpr` |
| `height` | uint32 | `0` | БАЗОВАЯ (логическая) высота в px; `0` = не задана (вычисляется пропорционально). Итоговый размер = `height × dpr`. Оба = `0` → исходный размер (`x`) |
| `output-formats` | list[string] | обязателен | **Массив** допустимых выходных форматов (whitelist): `jpeg\|png\|webp\|gif\|avif\|heif\|apng\|jxl`. Непустой; формат URL обязан входить в список |
| `dpr` | uint32 | ключ отсутствует | Множитель плотности пикселей. Имя с суффиксом `@N` — поле ОБЯЗАТЕЛЬНО и равно `N`. Имя без суффикса: не задан = wildcard-режим (`P.webp`, `P@2.webp`, `P@3.webp` допустимы в URL), `1` = фиксированный множитель (`@dpr` в URL запрещён), `2`/`3` = ошибка конфигурации. Подробнее — [правила dpr](#правила-dpr) |
| `crop` | string | `""` | `""`=resize, `center`=crop, `smart`=smart-crop, `face`=face-crop, `object`=object-crop |
| `trim` | bool | `false` | Обрезка однотонных полей. `crop`+`trim` — независимые фильтры: применяется сначала trim, затем кроп. Transform-код в URL не кодируется |
| `quality` | uint32 | `0` | 0–100; 0 = `processing.default-quality` |
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

### policy.path-policies

Политики по префиксам пути ассет-URL. **Map**: ключ = префикс пути, значение = настройки пути. Выбор policy — longest-prefix match; `"/"` — fallback для всех путей без более специфичного совпадения. deny-by-default: если для пути нет подходящей path-policy (нет `"/"` и совпадений) — запрос отклоняется.

| Поле | Тип | Описание |
|------|-----|----------|
| *(ключ)* | string | Префикс пути, нормализуется в `/prefix` (например `"basket/products"` → `"/basket/products"`); `"/"` — fallback |
| `presets` | list[string] | Список имён глобальных пресетов (`policy.presets`), доступных на этом пути; неизвестное имя — ошибка старта |
| `customs` | map | Custom-настройки пути: ключ = custom-имя, значение = настройки как у пресетов (см. [customs](#policypath-policiescustoms)) |

### policy.path-policies.*.customs

Custom-настройки пути: быстрый способ разрешить произвольные размеры без объявления пресета. **Имя** имеет размер-грамматику `x` (оригинал) / `x200` (только высота) / `200x` (только ширина) / `200x200` (точный размер), опционально с суффиксом `@2`/`@3` (например `200x100@2`). Настройки — те же, что у пресетов (`output-formats` обязателен, `crop`, `quality`, `dpr` и т.д.).

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

Поведение при включённом learning-mode:

- **Bypass admission**: запрос, не подходящий ни под одну path-policy, генерируется и отдаётся клиенту, если его сегмент — размер-грамматика (`120x60`, `x200`, `200x`, `x`). Сегмент-имя несуществующего пресета (например `banner`) остаётся `403` — learning-mode не «угадывает» пресеты.
- **Ничего не сохраняется**: даже ассеты, разрешённые path-policy, не публикуются в result-хранилище, пока режим включён. Уже сохранённые ассеты отдаются из кэша как обычно.
- **Автонакопление path-policies**: каждый обслуженный запрос с размер-сегментом наблюдается и записывается в `generate-local.yaml` (дебаунс ~2 с + финальная запись при graceful shutdown). Формат записи — `path-policies` с custom-размером и выходным форматом, например:

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

- **Требования**: для записи `generate-local.yaml` нужен каталог конфигурации (`IMAGER_CONFIG_DIR`); без него runtime-флаг работает, но наблюдения не сохраняются.
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
| `position` | string | `center` | `top\|bottom\|left\|right\|center` |
| `repeat` | string | `no-repeat` | `no-repeat\|repeat\|repeat-x\|repeat-y\|round\|space` |
| `size` | string | `contain` | `contain\|cover\|"200px 50px"` |

Ограничения движков: libvips поддерживает position/repeat/size полностью, включая покадровое наложение на анимированные выходы (GIF/WebP/APNG) с сохранением delay/loop. Все копии repeat/tile-раскладки накладываются одним composite-вызовом.

Приоритет применения водяного знака: пресет/custom (по имени из `policy.presets.<name>.watermark` / `policy.path-policies.*.customs.*.watermark`) → `processing.default-watermark`. Поле `watermark` у path-policy более не существует.

```yaml
watermarks:
  logo:
    path: "/etc/imager/watermarks/logo.png"
    position: bottom-right   # см. допустимые значения выше
    repeat: no-repeat
    size: contain
```

## processing

| Ключ | Тип | По умолчанию | Описание |
|------|-----|--------------|----------|
| `default-quality` | int | — | Качество по умолчанию (0–100) для lossy-форматов, когда quality не задан |
| `default-loop` | bool* | `true` | Зацикливание анимаций GIF/WebP/APNG/HEIF по умолчанию |
| `default-watermark` | string | пусто | Водяной знак по умолчанию (имя из `watermarks`) |
| `default-auto-orient` | bool* | `true` | Автоповорот по EXIF Orientation |
| `default-rotate` | string | `""` | Фиксированный поворот: `""`/`none`/`90`/`180`/`270` |
| `default-flip` | string | `""` | Отражение: `""`/`none`/`horizontal`/`vertical` |
| `default-trim-mode` | string | `"auto"` | Определение цвета фона для trim: `auto` (по краевому пикселю) или `color` |
| `default-trim-color` | string | пусто | Цвет фона trim в hex (`"#ffffff"`), только при `default-trim-mode: color` |
| `default-trim-tolerance` | float | `0` | Допуск сравнения пикселей с фоном `[0,1]` |
| `default-video-frame-percent` | int `[0,100]` | `50` | Процент от длительности видео, на котором выбирается кадр превью; `0` = кадр с начала |
| `default-video-min-contrast` | float `[0,1]` | `0.05` | Минимальная контрастность кадра; ниже — кадр считается неудачным и пропускается; `0` = проверка отключена |
| `default-video-frame-step` | int (≥1) | `1` | Шаг вперёд (в кадрах) при неудачной проверке контрастности |
| `default-video-attempts` | int (≥1) | `3` | Общее число попыток поиска подходящего кадра |

Порядок применения ориентации: auto-orient → rotate → flip, затем resize/crop/trim.

**Видео-источники** (`mp4`, `webm`, `mov`, `mkv`, `avi`, `m4v`): ассет строится из **одного кадра** видео, извлечённого через ffmpeg (видео процессоры не декодируют). Кадр сохраняется в result как `<видео-ключ>/x.jpg` и фиксируется в метаданных — следующие запросы используют сохранённый кадр, видео не открывается. При недостаточной контрастности кадра (`default-video-min-contrast`) поиск идёт с шагом `default-video-frame-step`, максимум `default-video-attempts` попыток.

## source / result

Хранилища исходников и результатов настраиваются независимо. Тип задаётся ключом `storage`.

Типы `fs`, `s3`, `sftp`, `ftp`, `ftps` доступны для source и result; `http` — только для source (ошибка старта для result). Детальное описание всех типов и параметров хранилищ — [STORAGE.md](STORAGE.md).

## libvips

Основной движок (govips, in-process). Секция актуальна для сборок с `-tags libvips`.

| Ключ (`libvips.limits.*`) | Тип | По умолчанию | Описание |
|---------------------------|-----|--------------|----------|
| `timeout` | duration | `"30s"` | Context deadline одной операции обработки |
| `output-bytes` | int | — | Лимит размера выходных данных (bounded writer) |
| `concurrency` | int | `16` (в коде при 0) | Максимум одновременных операций libvips |
| `threads` | int | `1` | Число потоков libvips (`vips_concurrency_set`) |
| `max-cache-mem` | int | 50 MiB | Максимум памяти кэша libvips |
| `max-cache-files` | int | default govips | Максимум открытых файлов кэша |
| `max-cache-size` | int | `100` | Максимум операций в кэше |

Per-format параметры сжатия кодировщиков (`libvips.encoders.*`). Значение `0` = встроенное умолчание движка. Диапазоны валидируются при старте: невалидное значение — ошибка конфигурации (fail-fast), не runtime-ошибка.

| Ключ (`libvips.encoders.*`) | Тип | По умолчанию | Описание |
|:---------------------------|------|--------------|----------|
| `webp-reduction-effort` | int `[0,6]` | `4` | Reduction effort WebP: больше = лучше сжатие, медленнее |
| `avif-speed` | int `[0,9]` | default govips (`5`) | Speed/effort AVIF: больше = быстрее, хуже сжатие |
| `png-compression-level` | int `[0,9]` | `6` | Уровень сжатия PNG (применяется и к APNG) |
| `jxl-effort` | int `[0,9]` | default govips (`7`) | Effort JPEG XL: больше = лучше сжатие, медленнее |
| `jpeg-progressive` | bool | `false` | Прогрессивный (interlaced) JPEG; `false` = baseline |
| `png-interlace` | bool | `false` | Чересстрочный (interlaced/Adam7) PNG; `false` = обычный |
| `png-palette` | bool | `false` | PNG-квантование (палитровый экспорт) для статичных PNG. Выключено по умолчанию: применяется только при явном включении; при ошибке — fallback на обычный PNG, запрос не падает. К APNG не применяется |
| `png-palette-colors` | int `[2,256]` | `256` | Максимум цветов палитры при `png-palette=true` (`0` = 256) |
| `png-palette-bit-depth` | int `[1,8]` | `8` | Битность палитры при `png-palette=true`; позволяет сохранить палитровую битность исходника (`0` = 8) |
| `gif-bit-depth` | int `[1,8]` | default govips (`8`) | Битность палитры GIF (`0` = умолчание govips) |

DPI-нормализация: при экспорте `xres`/`yres` сбрасываются к 72 DPI (после `stripAllMetadata`), чтобы просмотрщики не масштабировали изображение по DPI-метаданным исходника (например 300 DPI из сканера). Изображения уже с 72 DPI не перекопируются (быстрый путь). Константа не конфигурируется.

> **WebP preset**: в govips v2.18.0 `WebpExportParams` не содержит поля `Preset` (`default`/`photo`/`picture`/`drawing`/`icon`/`text`), поэтому параметр `webp-preset` не добавляется в конфиг — он пропущен до появления API в govips.

Shrink-on-load (`libvips.shrink-on-load.*`) — предварительное уменьшение при декодировании JPEG/WebP/GIF/HEIF/AVIF. Коэффициент вычисляется из целевого размера плана с запасом ×2 (после shrink размер гарантированно ≥ цели; точный resize выполняется далее как обычно). Решение консервативно: shrink НЕ применяется при trim/smart-crop/face-crop/object-crop, ручной ориентации или ненейтральном EXIF-повороте, `size=x`, неизвестных размерах исходника и для анимированных GIF. Для JPEG применяется shrink степени двойки (1/2, 1/4, 1/8), для WebP/HEIF/AVIF/GIF — scale-on-load.

| Ключ (`libvips.shrink-on-load.*`) | Тип | По умолчанию | Описание |
|:------------------------------------|------|--------------|----------|
| `enabled` | bool | `true` | Включить shrink-on-load при декодировании |

ICC color management (`libvips.color.mode`) — политика обработки embedded-ICC-профиля исходника. Проблема, которую решает: без color management цвета CMYK/ProPhoto/Display-P3 исходников искажаются (профиль просто удаляется). Режимы:

- `strip` (дефолт, обратная совместимость): профиль удаляется при обработке (`stripAllMetadata`).
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
  encoders:
    webp-reduction-effort: 4
    avif-speed: 6
    png-compression-level: 6
    jxl-effort: 0
    jpeg-progressive: false
    png-interlace: false
    png-palette: false
    png-palette-colors: 0
    png-palette-bit-depth: 0
    gif-bit-depth: 0
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
| `max-files` | int | `32` | Максимум записей (файлов) в кэше |
| `max-bytes` | int | 64 MiB | Суммарный бюджет памяти кэша в байтах |
| `ttl` | duration | `"5m"` | Страховочное время жизни записи |

Detection-семафор (`libvips.detection.*`) — отдельный bounded-семафор для тяжёлых CPU-bound ONNX-инференсов (face-crop/object-crop). Схема handoff: на время инференса libvips-слот освобождается и берётся detection-слот, после инференса слоты меняются обратно. Это защищает лёгкие операции (decode/resize/encode) от голодания при потоке fc/oc-запросов, сохраняя ограничение суммарной конкурентности (защита от OOM). Порядок захвата строго детерминирован (detection-слот берётся только при удержании libvips-слота), вложенного удержания обоих слотов во время долгих операций нет — дедлок невозможен. При переполнении очереди ожидания или истечении `max-wait` — быстрый отказ перегрузки.

| Ключ (`libvips.detection.*`) | Тип | По умолчанию | Описание |
|:-------------------------------|-----|--------------|----------|
| `concurrency` | int | `max(1, GOMAXPROCS/2)` | Максимум одновременных ONNX-инференсов |
| `max-wait` | duration | `"5s"` | Бюджет ожидания detection-слота; истечение → быстрый отказ |

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

Публикация результата в кэш выполняется фоновыми воркерами (подробнее — в [PROCESSING.md](PROCESSING.md), раздел «Асинхронная публикация»). Размер очереди/число воркеров задаются в параметрах построения composition-root (не через YAML) и по умолчанию равны 512 и 4; graceful drain при shutdown — с таймаутом 5 с.

Сбор отказоустойчив: паника/ошибка провайдера не влияет на обработку запросов (значения просто не обновляются до следующего тика); goroutine-сборщик останавливается при graceful shutdown.

| Ключ (`libvips.*`) | Тип | По умолчанию | Описание |
|:--------------------|:-----|--------------|----------|
| `metrics-interval` | duration | `"15s"` | Интервал сбора vips-метрик (минимум `"1s"`; `0` = дефолт) |

Примечания по производительности libvips-адаптера:

- **Лимит кадров анимации** (`frames` в пресетах/лимитах) применяется на этапе загрузки (`NumPages`), что дешевле пост-обрезки стека кадров.
- **Sequential access** выставляется при загрузке для операций с одним линейным проходом по пикселям (resize/crop/smart-crop без trim).
- **Premultiply**: перед resize изображений с альфа-каналом (PNG/WebP/GIF/APNG с прозрачностью) выполняется Premultiply → resize → Unpremultiply — исключает тёмные ореолы на полупрозрачных краях. Для анимаций операция применяется ко всему стеку кадров с сохранением delay/loop.

## detection

Детектор лиц/объектов для операций `fc`/`oc`. Требует сборки с `-tags onnx`. Включение операции задаётся путём к модели: пустой путь = операция отключена.

| Ключ | Тип | По умолчанию | Описание |
|------|-----|--------------|----------|
| `face-model` | string | пусто | Путь к ONNX-модели YuNet (лица) |
| `object-model` | string | пусто | Путь к ONNX-модели SSD/YOLO (объекты) |
| `onnx-runtime-lib` | string | пусто | Путь к библиотеке ONNX Runtime (libonnxruntime). Пусто = кроссплатформенная автодетекция (Linux `.so` / Windows `.dll` / macOS `.dylib`) по стандартным путям. Задаётся ТОЛЬКО через конфиг; env `ONNXRUNTIME_SHARED_LIBRARY_PATH` не используется |
| `confidence-threshold` | float | `0.5` | Порог уверенности детекции `[0,1]` |
| `max-objects` | int | `5` | Максимум объектов после NMS |
| `margin` | float | `0.1` | Отступ вокруг найденной области как доля её размера `[0,1]` |

Модели загружаются лениво при первом запросе и кэшируются в памяти до завершения процесса. Путь к библиотеке ONNX Runtime (`onnx-runtime-lib`) берётся из конфиг-файла, а не из env-переменной `ONNXRUNTIME_SHARED_LIBRARY_PATH`.

Автодетект библиотеки (`onnx-runtime-lib` пуст) кроссплатформенный и работает на Linux, Windows и macOS:

- **Linux** — ищутся `libonnxruntime.so.1.29.0`, `libonnxruntime.so`, `onnxruntime.so` и `libonnxruntime.so` в `/usr/lib/`, `/usr/lib/x86_64-linux-gnu/`, `/usr/local/lib/`, `/opt/onnxruntime/lib/`;
- **Windows** — ищется `onnxruntime.dll` (дефолт биндинга через `LoadLibrary`), рядом с исполняемым файлом, в `%WINDIR%\System32` и в каталоге установки ONNX Runtime (`%ProgramFiles%\onnxruntime\lib\`);
- **macOS** — ищутся `libonnxruntime.1.29.0.dylib` и `libonnxruntime.dylib` в `/usr/local/lib/`, `/opt/homebrew/lib/`, `/opt/onnxruntime/lib/`, а также голое имя `libonnxruntime.dylib`.

Приоритет: путь из конфига → первый существующий кандидат автодетекта → дефолт биндинга. Если ни один файл не найден, биндинг `github.com/yalue/onnxruntime_go` сам пробует `onnxruntime.so` (Linux/macOS) или `onnxruntime.dll` (Windows) через системные механизмы (ld.so / dyld / LoadLibrary).

## metadata

Sidecar-кэш результатов ИИ-детекции (лица/объекты) и `largest_ai_asset`: каждая модель вызывается один раз на родительский файл.

| Ключ | Тип | По умолчанию | Описание |
|------|-----|--------------|----------|
| `enabled` | bool | `true` | Включить sidecar-кэш |
| `dir` | string | `<result-каталог>` | Явный локальный путь метаданных; рекомендуется задавать при remote-result |

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
| `limits.concurrency` | uint32 | `0` | Максимум одновременных операций от одного клиента |
| `buffer-max-bytes` | int64 | `524288000` (500 MiB) | Бюджет памяти spillable-буфера (source+result); при исчерпании — спул на диск |

> Ключ `application.output-limit` удалён: заменён на `application.limits.output-bytes`. Лимиты перенесены из удалённой секции `policy.global.limits`.

## observability

| Ключ | Тип | По умолчанию | Описание |
|------|-----|--------------|----------|
| `log-level` | string | `"info"` | `debug\|info\|warn\|error` (регистронезависимо) |

### observability.asset-errors

Observability ошибок asset URL (неканонический URL, несуществующий пресет, недопустимый план, запрещённая политика). Включает счётчики, bounded top-paths и структурные логи.

| Ключ | Тип | По умолчанию | Описание |
|------|-----|--------------|----------|
| `enabled` | bool | `true` | Включать ли учёт ошибок asset URL (счётчики, top-paths, структурные логи). Дефолт `true` задаётся в коде (composition/runtimeconfig.go); в примере `setting.yaml` указан явно |
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
| `key-mode` | string | `"source"` | Режим ключа: `source` — путь исходника (если извлечь не удалось — raw URL); `hash` — sha256 первых 16 байт hex URL (не публикует пути исходников в метриках) |

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
| `token` | string | `""` | Bearer-токен для авторизации. |
| `workers` | int | `2` | Число параллельных фоновых генераций (≥ 1). |
| `queue-size` | int | `64` | Ёмкость очереди задач; переполнение → HTTP `503`. |
| `wait-timeout` | duration | `"300s"` | Таймаут режима `wait=true` (ожидания завершения всех ассетов до ответа); превышение → HTTP `504`. |

**Безопасность токена:** `token` — секрет. Храните его в `setting-local.yaml` (не коммитится, см. `.gitignore`) или в секрет-менеджере; не логируйте и не включайте в URL. Рекомендации — [SECURITY.md](SECURITY.md#админ-эндпоинты).

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

### `setting.yaml` (фундамент)

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

processing:
  default-quality: 85

libvips:
  encoders:
    webp-reduction-effort: 4
    avif-speed: 6
    png-compression-level: 6
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

Секреты и локальные отклонения от базовых файлов размещайте в `*-local.yaml`. Например, `setting-local.yaml`:

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
