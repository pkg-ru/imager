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
| **setting** (фундамент) | `setting.yaml` + `setting-local.yaml` | Инфраструктура сервера: HTTP-порт/таймауты, пути хранения, подключения к стораджам, observability/logging, безопасность, admin | Редко |
| **generate** (генерация) | `generate.yaml` + `generate-local.yaml` | Настройки генерации ассетов: пресеты, policy, форматы/энкодеры, ресайз, watermark, orientation, trim, color, detection | Часто |
| **failback** (резервы) | `failback.yaml` + `failback-local.yaml` | Резервные/необязательные fallback-механизмы: ImageMagick, not-found, source-fallback | Почти никогда |

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
| `http.not-found`, `not-found-cache-control`, `source-fallback` | failback | `failback.yaml` |
| `source`, `result` | setting | `setting.yaml` |
| `libvips.limits`, `libvips.operation-cache`, `libvips.metrics-interval` | setting | `setting.yaml` |
| `libvips.encoders`, `shrink-on-load`, `color`, `watermark-cache`, `detection` | generate | `generate.yaml` |
| `metadata` | setting | `setting.yaml` |
| `application.buffer-max-bytes` | setting | `setting.yaml` |
| `application.output-limit` | generate | `generate.yaml` |
| `observability` | setting | `setting.yaml` |
| `admin` | setting | `setting.yaml` |
| `policy` (global, presets, path-policies) | generate | `generate.yaml` |
| `watermarks` | generate | `generate.yaml` |
| `processing` | generate | `generate.yaml` |
| `detection` | generate | `generate.yaml` |
| `imagemagick` | failback | `failback.yaml` |

> Секция `http` — единственная, чьи подсекции расходятся по слоям: транспортные/security-ключи живут в `setting.yaml`, а fallback-подсекции (`not-found`, `not-found-cache-control`, `source-fallback`) — в `failback.yaml`. Благодаря deep merge подсекции одного top-level ключа из разных файлов корректно объединяются.

Полные самодокументированные примеры — [`config/setting.yaml`](../config/setting.yaml), [`config/generate.yaml`](../config/generate.yaml), [`config/failback.yaml`](../config/failback.yaml); локальные переопределения — [`config/setting-local.yaml`](../config/setting-local.yaml), [`config/generate-local.yaml`](../config/generate-local.yaml), [`config/failback-local.yaml`](../config/failback-local.yaml).

## Обратная совместимость

Старый монолитный `setting.yaml`, содержащий все секции (включая «переехавшие» в generate/failback), продолжает работать без изменений: merge выполняется на уровне map до strict-декодирования, а схема едина. Если новый `generate.yaml`/`failback.yaml` дублирует секцию из старого `setting.yaml` — применяется deep merge в порядке `setting → generate → failback` (значение из более специализированного слоя побеждает) с warning в лог. Миграция сводится к механическому переносу секций между файлами согласно таблице выше.

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
| `enabled` | bool | `false` | Включать ли source fallback. Выключен по умолчанию (текущее поведение). |
| `status` | int | `404` | HTTP-статус ответа: `200` или `404` (0 → `404`). |
| `cache-control` | string | `"no-store"` | `Cache-Control` для source fallback-ответа. |

Когда fallback срабатывает, вместо пикселя/ошибки отдаётся исходный файл с его оригинальными `Content-Type`/`Content-Disposition`/`Cache-Control`/`ETag` (см. [API.md](API.md#source-fallback)).

**Когда использовать:** если CDN/браузеры кешируют тысячи разных ошибочных ответов (несуществующие пресеты, неканонические URL, запрещённые политикой запросы), это раздувает кэш и создаёт нагрузку. Source fallback отдаёт один и тот же исходный файл с настраиваемым статусом, чтобы CDN не кешировал множество различных ошибочных ответов.

**Выбор `200` vs `404`:**

- `status: 200` — ответ кешируется как успешный. Подходит, если вы хотите, чтобы CDN отдавал исходный файл как «запасной» вариант для любых ошибочных URL. Внимание: `200` скрывает ошибки от клиентов и мониторинга (все ошибочные запросы выглядят как успешные).
- `status: 404` (по умолчанию) — ответ кешируется как ошибочный. CDN/браузеры не будут долго кешировать `404` (особенно с `cache-control: no-store`), а мониторинг продолжит видеть ошибки. Рекомендуется для production.

**Предупреждение про CDN-кеширование:** при `status: 200` убедитесь, что `cache-control` не слишком долгий, иначе ошибочные URL будут кешироваться как успешные на длительный срок. По умолчанию `no-store` — безопасно.

```yaml
http:
  source-fallback:
    enabled: false
    status: 404
    cache-control: "no-store"
```

## policy

Deny-by-default политика. Всё запрещено, кроме явно разрешённого. Подробности семантики — [SECURITY.md](SECURITY.md).

### policy.global

| Ключ | Тип | По умолчанию | Описание |
|------|-----|--------------|----------|
| `authorization` | string | — | `"safe"` — только пресеты из whitelist и размеры, покрытые `size-rules`; `"unsafe"` — любые параметры (лимиты продолжают действовать) |
| `allowed-presets` | list[string] | пусто | Whitelist имён пресетов целиком, включая суффикс: `"thumb@2"`; игнорируется при `authorization: "unsafe"` |
| `size-rules` | list[string] | пусто | Правила размеров `"minW-maxWxminH-maxH"`; пустая сторона = любая; `"500x"` = точная ширина 500; пустой список в safe = все канонические запросы отклоняются |

### policy.global.limits

Лимиты применяются в любом режиме авторизации. `0` = без ограничения.

| Ключ | Тип | Описание |
|------|-----|----------|
| `source-bytes` | int | Максимум размера исходного файла (байт) |
| `output-bytes` | int | Максимум размера выходного файла (байт) |
| `width` / `height` | int | Максимум ширины/высоты запроса (px) |
| `pixels` | int | Максимум пикселей (w×h) |
| `dpr` | int | Максимум DPR запроса |
| `frames` | int | Максимум кадров анимации |
| `duration` | int | Максимум длительности анимации (мс) |
| `concurrency` | int | Максимум одновременных операций от одного клиента |

### policy.presets

Именованные конфигурации обработки. Пресет обязан быть включён в `policy.global.allowed-presets`, иначе недоступен в URL.

| Поле | Тип | По умолчанию | Описание |
|------|-----|--------------|----------|
| `name` | string | обязателен | ≤64 символа, без дефисов; допустимы буквы, цифры, `_`, `.`, `@`; уникально |
| `crop` | string | `""` | `""`=resize, `center`=crop, `smart`=smart-crop, `face`=face-crop, `object`=object-crop |
| `trim` | bool | `false` | Обрезка однотонных полей перед кропом/ресайзом |
| `size` | string | обязателен | `"ШxВ"`; одна сторона может отсутствовать (`"x400"`, `"300x"`); `"x"` = исходный размер |
| `output-format` | string | обязателен | `jpeg\|png\|webp\|gif\|avif\|heif\|apng\|jxl` |
| `quality` | int | `0` | 0–100; 0 = `processing.default-quality` |
| `dpr` | int | `0` | 0/1/2/3; 0 = берётся из суффикса `@dpr` имени или URL |
| `frames` | int | `0` | Максимум кадров анимации; 0 = без ограничения |
| `duration` | int | `0` | Максимум длительности анимации (мс) |
| `loop` | bool* | nil | nil = `processing.default-loop`; true = бесконечная анимация |
| `watermark` | string | пусто | Имя ватермарки из секции `watermarks` |
| `auto-orient` | bool* | nil | Автоповорот по EXIF; nil = глобальный дефолт |
| `rotate` | string | `""` | `""`/`none`/`90`/`180`/`270` |
| `flip` | string | `""` | `""`/`none`/`horizontal`/`vertical` |

Комбинация `crop`+`trim` кодируется в transform-код URL: `resize`, `c`, `t`, `ct`, `sc`, `sct`, `fc`, `fct`, `oc`, `oct`.

Пример:

```yaml
presets:
  - name: "thumb"
    size: "200x200"
    output-format: webp
    quality: 85
    dpr: 1
  - name: "portrait"
    crop: face
    trim: true
    size: "300x300"
    output-format: jpeg
```

### policy.path-policies

Политики по префиксам пути канонических URL. Применяются только к каноническим URL (не к пресетам) и могут **только ужесточать** глобальную политику. Выбор — longest prefix match; `"/"` — fallback для всех путей.

| Поле | Тип | Описание |
|------|-----|----------|
| `path` | string | Префикс пути (нормализуется в `/prefix`) |
| `dpr` | string | Диапазон допустимых DPR (`"0-1"`, `"2-3"`); пусто = без ограничения |
| `crop` | string/list | Строка: единственный разрешённый режим (`center`→`c/ct`, `smart`→`sc/sct`, `face`→`fc/fct`, `object`→`oc/oct`, `none` — кроп запрещён); список: whitelist режимов |
| `trim` | bool* | nil = неважно; true = trim обязателен; false = trim запрещён |
| `watermark` | string | Ватермарка префикса пути (приоритет ниже пресета, выше дефолта) |

Пример:

```yaml
path-policies:
  - path: "/"
  - path: "/thumbs"
    dpr: "0-1"
    crop: center
  - path: "/originals"
    crop: none
    trim: false
```

## watermarks

Именованные декларации ватермарок. Секция опциональна.

| Поле | Тип | По умолчанию | Описание |
|------|-----|--------------|----------|
| `name` | string | обязателен | Уникальное имя для ссылок |
| `path` | string | обязателен | Путь к PNG-файлу на диске; отсутствие файла — ошибка старта |
| `position` | string | `center` | `top\|bottom\|left\|right\|center` |
| `repeat` | string | `no-repeat` | `no-repeat\|repeat\|repeat-x\|repeat-y\|round\|space` |
| `size` | string | `contain` | `contain\|cover\|"200px 50px"` |

Ограничения движков: ImageMagick поддерживает точный размер только в px-форме и все repeat-режимы рендерит сплошной плиткой; libvips поддерживает position/repeat/size полностью, включая покадровое наложение на анимированные выходы (GIF/WebP/APNG) с сохранением delay/loop. Все копии repeat/tile-раскладки накладываются одним composite-вызовом.

Приоритет применения ватермарки: пресет → path-policy → `processing.default-watermark`.

```yaml
watermarks:
  - name: logo
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
| `default-watermark` | string | пусто | Ватермарка по умолчанию (имя из `watermarks`) |
| `default-auto-orient` | bool* | `true` | Автоповорот по EXIF Orientation |
| `default-rotate` | string | `""` | Фиксированный поворот: `""`/`none`/`90`/`180`/`270` |
| `default-flip` | string | `""` | Отражение: `""`/`none`/`horizontal`/`vertical` |
| `default-trim-mode` | string | `"auto"` | Определение цвета фона для trim: `auto` (по краевому пикселю) или `color` |
| `default-trim-color` | string | пусто | Цвет фона trim в hex (`"#ffffff"`), только при `default-trim-mode: color` |
| `default-trim-tolerance` | float | `0` | Допуск сравнения пикселей с фоном `[0,1]` |

Порядок применения ориентации: auto-orient → rotate → flip, затем resize/crop/trim.

## source / result

Хранилища исходников и результатов настраиваются независимо. Тип задаётся ключом `storage`.

Поддерживаемые типы:

| Тип | Source | Result | Примечание |
|-----|--------|--------|------------|
| `fs` | ✅ | ✅ | Локальная файловая система, ключ `path` |
| `s3` | ✅ | ✅ | S3 и совместимые (MinIO, Yandex Object Storage) |
| `sftp` | ✅ | ✅ | Обязателен `host-key-fingerprint` |
| `ftp` | ✅ | ✅ | Plain FTP |
| `ftps` | ✅ | ✅ | Explicit TLS; `tls-verify: false` запрещён |
| `http` | ✅ | ❌ (ошибка старта) | Только чтение |

Детальное описание всех параметров хранилищ — [STORAGE.md](STORAGE.md).

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

Shrink-on-load (`libvips.shrink-on-load.*`) — предварительное уменьшение при декодировании JPEG/WebP/GIF/HEIF/AVIF. Коэффициент вычисляется из целевого размера плана с запасом ×2 (после shrink размер гарантированно ≥ цели; точный resize выполняется далее как обычно). Решение консервативно: shrink НЕ применяется при trim/smart-crop/face-crop/object-crop, ручной ориентации или ненейтральном EXIF-повороте, `size=x`, неизвестных размерах исходника и для анимированных GIF.

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

Passthrough fast-path: в режиме `transform` исходники с sRGB-совместимым профилем могут возвращаться без перекодирования (конверсия была бы no-op); в режиме `keep` passthrough допустим для любого профиля (профиль сохраняется в выходе). EXIF/XMP/IPTC и прочие метаданные по-прежнему блокируют passthrough.

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

Кэш файлов ватермарок (`libvips.watermark-cache.*`) — in-memory кэш исходных БАЙТОВ файлов ватермарок, keyed по пути файла (не по конфигурации наложения): один файл обслуживает любое число настроек позиции/масштаба/repeat. Декодирование выполняется libvips на каждый запрос (ускоряется его operation cache), чтение с диска устраняется. Инвалидация записи — по mtime/размеру файла (лёгкий `stat` на каждый запрос) плюс TTL; вытеснение — LRU по числу записей и суммарному бюджету байтов; параллельная загрузка одного файла дедуплицируется (singleflight). При промахе кэша выполняется прозрачное чтение с диска — запрос не ломается. Файл больше `max-bytes` не кэшируется.

| Ключ (`libvips.watermark-cache.*`) | Тип | По умолчанию | Описание |
|:------------------------------------|-----|--------------|----------|
| `enabled` | bool | `true` | Включить кэш; `false` = каждое использование читает диск |
| `max-files` | int | `32` | Максимум записей (файлов) в кэше |
| `max-bytes` | int | 64 MiB | Суммарный бюджет памяти кэша в байтах |
| `ttl` | duration | `"5m"` | Страховочное время жизни записи |

Detection-семафор (`libvips.detection.*`) — отдельный bounded-семафор для тяжёлых CPU-bound ONNX-инференсов (face-crop/object-crop). Схема handoff (Фаза 4): на время инференса libvips-слот освобождается и берётся detection-слот, после инференса слоты меняются обратно. Это защищает лёгкие операции (decode/resize/encode) от голодания при потоке fc/oc-запросов, сохраняя ограничение суммарной конкурентности (защита от OOM). Порядок захвата строго детерминирован (detection-слот берётся только при удержании libvips-слота), вложенного удержания обоих слотов во время долгих операций нет — дедлок невозможен. При переполнении очереди ожидания или истечении `max-wait` — быстрый отказ перегрузки.

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
- `imager_vips_watermark_cache_hits_total` / `imager_vips_watermark_cache_misses_total` / `imager_vips_watermark_cache_entries` / `imager_vips_watermark_cache_bytes` — метрики кэша ватермарок.

Сбор отказоустойчив: паника/ошибка провайдера не влияет на обработку запросов (значения просто не обновляются до следующего тика); goroutine-сборщик останавливается при graceful shutdown.

| Ключ (`libvips.*`) | Тип | По умолчанию | Описание |
|:--------------------|:-----|--------------|----------|
| `metrics-interval` | duration | `"15s"` | Интервал сбора vips-метрик (минимум `"1s"`; `0` = дефолт) |

Примечания по производительности libvips-адаптера:

- **Passthrough**: если план обработки ничего не меняет (целевой формат совпадает с исходным, размер уже целевой или `size=x`, нет watermark/trim/ориентации/детекции/ограничений кадров, исходник без EXIF/XMP/ICC-метаданных) — исходные байты возвращаются как есть, без decode/encode. В режиме `libvips.color.mode: transform` passthrough дополнительно допускается для исходников с sRGB-совместимым ICC-профилем (конверсия была бы no-op); в режиме `keep` — для любого профиля.
- **Лимит кадров анимации** (`frames` в пресетах/лимитах) применяется на этапе загрузки (`NumPages`), что дешевле пост-обрезки стека кадров.
- **Sequential access** выставляется при загрузке для операций с одним линейным проходом по пикселям (resize/crop/smart-crop без trim).
- **Shrink-on-load**: для JPEG применяется shrink степени двойки (1/2/1/4/1/8), для WebP/HEIF/AVIF/GIF — scale-on-load. Отключается ключом `libvips.shrink-on-load.enabled: false`.
- **ICC color management** (`libvips.color.mode: transform`): embedded-профиль конвертируется в sRGB через PCS перед обработкой (не после!). Fast-path исключает конверсию для sRGB-совместимых профилей и изображений уже в sRGB — нулевой оверхед для большинства веб-исходников. Ошибки lcms не роняют запрос (fallback на strip).
- **Operation cache** (`libvips.operation-cache.enabled: false`): при отключении в Startup передаются нулевые лимиты кэша (`vips_cache_set_max_mem(0)`/`vips_cache_set_max(0)`/`vips_cache_set_max_files(0)`) — в govips `0` означает полное отключение кэша, а не «без лимита». Рекомендуется для stateless-обработчика (меньше памяти, меньше риска на musl/Alpine).
- **Premultiply**: перед resize изображений с альфа-каналом (PNG/WebP/GIF/APNG с прозрачностью) выполняется Premultiply → resize → Unpremultiply — исключает тёмные ореолы на полупрозрачных краях. Для анимаций операция применяется ко всему стеку кадров с сохранением delay/loop.

## detection

Детектор лиц/объектов для операций `fc`/`oc`. Требует сборки с `-tags onnx`. Включение операции задаётся путём к модели: пустой путь = операция отключена.

| Ключ | Тип | По умолчанию | Описание |
|------|-----|--------------|----------|
| `face-model` | string | пусто | Путь к ONNX-модели YuNet (лица) |
| `object-model` | string | пусто | Путь к ONNX-модели SSD/YOLO (объекты) |
| `confidence-threshold` | float | `0.5` | Порог уверенности детекции `[0,1]` |
| `max-objects` | int | `5` | Максимум объектов после NMS |
| `margin` | float | `0.1` | Отступ вокруг найденной области как доля её размера `[0,1]` |

Модели загружаются лениво при первом запросе и кэшируются в памяти до завершения процесса.

## imagemagick

Опциональный fallback для сборок без `-tags libvips`. В сборках с libvips не создаётся и не запускается.

| Ключ | Тип | По умолчанию | Описание |
|------|-----|--------------|----------|
| `binary` | string | `"magick"` | Имя или путь к бинарю; для ImageMagick 6 укажите `"convert"` |

### imagemagick.policy — генерация deny-by-default policy.xml

| Ключ | Тип | По умолчанию | Описание |
|------|-----|--------------|----------|
| `enabled` | bool* | `true` | Генерировать policy.xml |
| `dir` | string | пусто | Каталог для policy.xml; пусто = временный каталог (удаляется при закрытии) |
| `disable-network` | bool* | `true` | Запретить network-capable delegates (SSR-риск); не отключайте в production |
| `disabled-coders` | list[string] | пусто | Дополнительные coders для запрета |
| `disabled-delegates` | list[string] | пусто | Дополнительные delegates для запрета |
| `max-memory-bytes` / `max-map-bytes` / `max-disk-bytes` | int | — | Resource-лимиты policy.xml (0 = не задавать) |
| `max-threads` / `max-time-seconds` | int | — | Лимиты потоков и времени |
| `max-width` / `max-height` / `max-pixels` / `max-frames` | int | — | Защита от decompression bomb |

Генерируемая политика запрещает все coders/delegates по умолчанию и разрешает только безопасный whitelist (JPEG/PNG/WebP/GIF/AVIF/HEIC/HEIF/APNG/MIFF/PPM/PGM/PBM/PNM/TIFF/BMP/ICO), явно блокирует network- и scripting-coders (URL/HTTPS/FTP/MSL/MVG/LABEL/TEXT/PS/PDF/SVG и др.) и delegates (curl, wget, ssh, rsvg, inkscape…).

### imagemagick.limits — лимиты subprocess

| Ключ | Тип | По умолчанию | Описание |
|------|-----|--------------|----------|
| `timeout` | duration | `"30s"` | Context deadline одного subprocess (убийство процесса) |
| `output-bytes` | int | — | Лимит выходных данных (bounded writer на stdout) |
| `memory-bytes` / `map-bytes` / `disk-bytes` | int | — | `-limit memory/map/disk` (0 = не задавать) |
| `threads` | int | авто | Число потоков |
| `time-seconds` | int | — | Лимит времени выполнения |
| `width` / `height` / `pixels` | int | — | Защита от decompression bomb |
| `frames` | int | — | Лимит кадров анимации |
| `concurrency` | int | `16` (в коде при 0) | Максимум одновременных subprocess |
| `webp-method` | int | `4` | Метод сжатия WebP (0–6) |
| `png-compression-level` | int | `6` | Уровень сжатия PNG (0–9) |

## metadata

Sidecar-кэш результатов ИИ-детекции (лица/объекты) и `largest_ai_asset`: каждая модель вызывается один раз на родительский файл.

| Ключ | Тип | По умолчанию | Описание |
|------|-----|--------------|----------|
| `enabled` | bool | `true` | Включить sidecar-кэш |
| `dir` | string | `<result-каталог>` | Явный локальный путь метаданных; рекомендуется задавать при remote-result |

Метаданные всегда хранятся локально, независимо от типов source/result.

> **Пакетные удаления и S3.** Если для результата используется S3-хранилище, `metadata.dir` лучше указывать **локально** (на локальном диске), а не в S3. Тогда пакетное удаление ассетов в S3 не затрагивает sidecar-метаданные — они остаются на локальном диске и не удаляются вместе с объектами S3.

## application

| Ключ | Тип | По умолчанию | Описание |
|------|-----|--------------|----------|
| `output-limit` | int64 | `0` | Максимум размера сохраняемого выходного файла (байт); превышение прерывает генерацию |
| `buffer-max-bytes` | int64 | `524288000` (500 MiB) | Бюджет памяти spillable-буфера (source+result); при исчерпании — спул на диск |

## observability

| Ключ | Тип | По умолчанию | Описание |
|------|-----|--------------|----------|
| `log-level` | string | `"info"` | `debug\|info\|warn\|error` (регистронезависимо) |

### observability.asset-errors

Observability ошибок asset URL (неканонический URL, несуществующий пресет, недопустимый план, запрещённая политика). Включает счётчики, bounded top-paths и структурные логи.

| Ключ | Тип | По умолчанию | Описание |
|------|-----|--------------|----------|
| `enabled` | bool | `true` | Включать ли учёт ошибок asset URL (счётчики, top-paths, структурные логи) |
| `log-level` | string | `"warn"` | Уровень структурного лога ошибки: `debug\|info\|warn\|error` |

При `enabled: true` ошибки фиксируются:

- **счётчиком** `imager_asset_errors` в `/metrics` и `/debug/vars` — по категории `kind` (`parse` | `preset_not_found` | `invalid_plan` | `policy_denied`);
- **структурными логами** с полями `kind`, `url`, `preset`, `reason` на уровне `log-level`;
- **top-paths** — при включённом `top-paths` (см. ниже).

#### observability.asset-errors.top-paths

Bounded LRU-реестр проблемных путей с вытеснением. Кардинальность ограничена `max-entries`, поэтому не раздувает память даже при большом числе уникальных ошибочных URL.

| Ключ | Тип | По умолчанию | Описание |
|------|-----|--------------|----------|
| `enabled` | bool | `false` | Включать ли учёт top-paths |
| `max-entries` | int | `1024` | Максимальное число отслеживаемых путей (LRU; при превышении вытесняется наименее недавно использованный) |
| `report-top` | int | `20` | Число путей в отчёте (Top(n)) |
| `key-mode` | string | `"source"` | Режим ключа: `source` (путь исходника) или `hash` (sha256, первые 16 байт hex) |

- `key-mode: source` — ключ — путь исходника (извлекается из URL); если извлечь не удалось — raw URL.
- `key-mode: hash` — ключ — sha256-хэш первых 16 байт URL (hex). Полезно, когда не хотите публиковать пути исходников в метриках.

Отчёт топ-`report-top` путей публикуется в `/debug/vars` (expvar) и доступен в `/metrics`.

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

Административные эндпоинты для управления ассетами: фоновая генерация всех/выбранных ассетов исходника и удаление ассетов. **Выключены по умолчанию** (`enabled: false`). При включении обязателен непустой `token`, иначе старт завершится ошибкой (fail-fast).

Маршруты (регистрируются в mux только при `enabled: true`):

- `POST /admin/assets/generate` — генерация ассетов;
- `DELETE /admin/assets/delete` — удаление ассетов.

Авторизация — заголовок `Authorization: Bearer <token>` (constant-time сравнение через `crypto/subtle`). Неверный/отсутствующий токен → `403`. Подробности API — [API.md](API.md#админ-эндпоинты).

| Ключ | Тип | По умолчанию | Описание |
|------|-----|--------------|----------|
| `enabled` | bool | `false` | Включать ли admin-эндпоинты. Выключены по умолчанию. |
| `token` | string | `""` | Bearer-токен для авторизации. **Обязателен** при `enabled: true` (иначе fail-fast ошибка старта). |
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
  buffer-max-bytes: 524288000

observability:
  log-level: "warn"
```

### `generate.yaml` (генерация ассетов)

```yaml
policy:
  global:
    authorization: "safe"
    allowed-presets: ["thumb", "thumb@2"]
    size-rules: ["0-2000x0-2000"]
    limits:
      source-bytes: 10485760
      output-bytes: 10485760
  presets:
    - name: "thumb"
      size: "200x200"
      output-format: webp
      quality: 85
      dpr: 1
    - name: "thumb@2"
      size: "400x400"
      output-format: webp
      quality: 85
      dpr: 2
  path-policies:
    - path: "/"

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
  output-limit: 10485760
```

### `failback.yaml` (резервные механизмы)

```yaml
imagemagick:
  binary: "magick"
  policy:
    enabled: true
    disable-network: true
  limits:
    timeout: "30s"
    output-bytes: 10485760
    concurrency: 2

http:
  not-found-cache-control: "no-store"
  not-found:
    pixel: true
  source-fallback:
    enabled: false
    status: 404
    cache-control: "no-store"
```

### Локальные переопределения

Секреты и локальные отклонения от базовых файлов размещайте в `*-local.yaml` (не коммитятся, см. `.gitignore`). Например, `setting-local.yaml`:

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
