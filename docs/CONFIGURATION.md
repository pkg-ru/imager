# Конфигурация

Все настройки задаются в YAML. Прикладных env-переменных и CLI-флагов нет.

## Загрузка конфигурации

Единственная переменная окружения:

| Переменная | По умолчанию | Описание |
|------------|--------------|----------|
| `IMAGER_CONFIG_DIR` | `.` | Каталог с файлами конфигурации |

Внутри каталога читаются:

- `setting.yaml` — **обязательный** базовый конфиг; отсутствие или невалидность останавливает старт;
- `setting-local.yaml` — **опциональный** локальный конфиг, глубоко переопределяющий базовый:
  - вложенные map мержатся рекурсивно (ключи, не указанные в local, сохраняются);
  - скаляры заменяются значением из local;
  - списки заменяются **целиком** (дополнить список из local нельзя).

Декодирование строгое (`yaml.UnmarshalStrict`): любой ключ вне схемы — ошибка старта. Неверное значение `version` (актуальна `"1"`) — ошибка старта.

Секреты (пароли, ключи S3/SFTP) рекомендуется размещать в `setting-local.yaml`, который не коммитится (см. `.gitignore`). Для S3 также доступны env `IMAGER_S3_ACCESS_KEY` / `IMAGER_S3_SECRET_KEY`.

## Схема верхнего уровня

```yaml
version: "1"
server:          # HTTP/TCP сервер
http:            # HTTP-адаптер: CORS, кэш-заголовки, not-found fallback
policy:          # deny-by-default политика авторизации и лимитов
watermarks:      # именованные декларации ватермарок
processing:      # умолчания обработки
source:          # хранилище исходников
result:          # хранилище результатов
libvips:         # основной движок обработки
detection:       # детектор лиц/объектов (ONNX)
imagemagick:     # опциональный fallback-движок
metadata:        # sidecar-кэш метаданных ИИ-детекции
application:     # прикладные лимиты
observability:   # логирование
```

Полный самодокументированный пример — [`config/setting.yaml`](../config/setting.yaml); локальные переопределения — [`config/setting-local.yaml`](../config/setting-local.yaml).

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

Ограничения движков: ImageMagick поддерживает точный размер только в px-форме и все repeat-режимы рендерит сплошной плиткой; анимированные выходы (GIF/WebP/APNG) с ватермаркой на libvips возвращают ошибку обработки.

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

```yaml
libvips:
  limits:
    timeout: "30s"
    output-bytes: 10485760
    concurrency: 2
    threads: 4
    max-cache-mem: 52428800
```

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
| `dir` | string | `<result-каталог>/.meta` | Явный локальный путь метаданных; рекомендуется задавать при remote-result |

Метаданные всегда хранятся локально, независимо от типов source/result.

## application

| Ключ | Тип | По умолчанию | Описание |
|------|-----|--------------|----------|
| `output-limit` | int64 | `0` | Максимум размера сохраняемого выходного файла (байт); превышение прерывает генерацию |
| `buffer-max-bytes` | int64 | `524288000` (500 MiB) | Бюджет памяти spillable-буфера (source+result); при исчерпании — спул на диск |

## observability

| Ключ | Тип | По умолчанию | Описание |
|------|-----|--------------|----------|
| `log-level` | string | `"info"` | `debug\|info\|warn\|error` (регистронезависимо) |

## Пример production-переопределения (setting-local.yaml)

```yaml
server:
  addr: ":9090"

http:
  allowed-origins:
    - "https://cdn.example.com"

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

observability:
  log-level: "warn"
```
