# Imager

HTTP-сервис динамической обработки изображений по каноническим URL. Исходные файлы читаются из подключённого хранилища (локальный диск, S3, SFTP, FTP/FTPS, HTTP), результат обрабатывается on-the-fly, кэшируется и отдаётся с immutable-заголовками.

## Возможности

- **Форматы**: JPEG, PNG, WebP, GIF, AVIF, HEIF/HEIC, APNG, JPEG XL — на входе и на выходе. Конвертация между любыми форматами.
- **Операции обработки**:
  - resize (`120x80`, `x400`, `300x`, `x` — исходный размер);
  - центрированный crop (`c`);
  - smart-crop по attention-области (`sc`);
  - face-crop по детекции лиц (`fc`);
  - object-crop по детекции объектов (`oc`);
  - trim — обрезка однотонных полей (`t`), комбинируется с любым кропом (`ct`, `sct`, `fct`, `oct`);
  - автоматический поворот по EXIF Orientation, фиксированный rotate (90/180/270) и flip (horizontal/vertical);
  - водяные знаки с позиционированием в стиле CSS (`position`/`repeat`/`size`).
- **DPR (Retina)**: суффикс `@2`/`@3` умножает целевой размер на device pixel ratio.
- **Пресеты**: именованные конфигурации обработки (`/test-jpg/thumb.webp`) с фиксацией размера, формата, качества, DPR, лимитов анимации и ватермарки.
- **Детекция объектов**: ONNX Runtime внутри процесса — модели YuNet (лица) и SSD/YOLO-подобная (объекты); результаты детекции кэшируются в локальном sidecar-хранилище метаданных.
- **Политика безопасности deny-by-default**: whitelist пресетов, правила допустимых размеров, per-path политики (longest prefix match) по DPR/crop/trim/watermark, лимиты на размер источника и результата, DPR, кадры и длительность анимаций.
- **Хранилища**: source и result настраиваются независимо — `fs`, `s3` (включая MinIO/Yandex Object Storage), `sftp`, `ftp`, `ftps`, `http` (только source). Атомарная публикация результатов (temp+rename), retry с экспоненциальным backoff, пулы соединений.
- **Производительность**: keyed singleflight — параллельные запросы одного ассета дедуплицируются; spillable-буферы (память с переполнением на диск); LRU-кэш ETag; admission control по числу одновременных запросов.
- **Движки обработки**: libvips (govips, in-process) как основной; ImageMagick как опциональный fallback для сборок без тега `libvips`.
- **HTTP**: GET/HEAD/OPTIONS, CORS allowlist, ETag + условные запросы (304), настраиваемый Cache-Control, not-found fallback (пиксель в формате запроса / картинка / HTML-страница / редирект).
- **Observability**: `/healthz`, `/readyz`, `/metrics` (Prometheus exposition format, bounded cardinality), структурированные JSON-логи с request ID; URL и секреты не логируются.
- **Эксплуатация**: graceful shutdown, janitor для очистки осиротевших temp-файлов, квоты хранилища, fail-fast валидация конфигурации.

## Быстрый старт

### Docker Compose

```bash
docker compose up -d --build
```

Конфигурация монтируется из `./config` в `/etc/imager` (read-only). Сервис слушает `:8080`.

### Локально (Go)

Требуется libvips ≥ 8.13 с заголовками (`vips-dev`) и C-компилятор:

```bash
go build -tags libvips -trimpath -ldflags="-s -w" -o imager ./cmd/imager
IMAGER_CONFIG_DIR=. ./imager
```

Без тега `libvips` сервис использует ImageMagick (`magick` в PATH).

### Минимальный конфиг

`config/setting.yaml` (сокращённый вариант; полный самодокументированный файл уже в репозитории):

```yaml
version: "1"

server:
  addr: ":8080"

http:
  cache-control: "public, max-age=2592000"
  not-found:
    pixel: true

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

source:
  storage: fs
  path: "./data/source"

result:
  storage: fs
  path: "./data/result"

processing:
  default-quality: 85
```

Единственная переменная окружения — `IMAGER_CONFIG_DIR` (каталог с `setting.yaml`; опциональный `setting-local.yaml` глубоко переопределяет базовый).

### Примеры запросов

Положите файл `test.jpg` в каталог `./data/source`, затем:

```bash
# Канонический URL: crop 120x80, DPR 2, WebP
curl -o out.webp http://localhost:8080/test-jpg/c-120x80@2.webp

# Только ресайз до ширины 640
curl -o out.webp http://localhost:8080/test-jpg/640x.webp

# Пресет "thumb" (200x200 WebP)
curl -o thumb.webp http://localhost:8080/test-jpg/thumb.webp

# Пресет "thumb@2" (400x400 WebP)
curl -o thumb2.webp http://localhost:8080/test-jpg/thumb@2.webp

# Trim однотонных полей + центрированный кроп 300x300, AVIF
curl -o out.avif http://localhost:8080/test-jpg/ct-300x300.avif

# Проверка живости
curl http://localhost:8080/healthz
```

Формат URL: `/{path}/{source_name}-{source_format}/{transform}-{size}@{dpr}.{output_format}` — полный справочник в [docs/API.md](docs/API.md).

## Документация

| Документ | Содержание |
|----------|------------|
| [docs/INSTALLATION.md](docs/INSTALLATION.md) | Сборка, зависимости, Docker |
| [docs/CONFIGURATION.md](docs/CONFIGURATION.md) | Все параметры `config/setting.yaml` |
| [docs/API.md](docs/API.md) | Формат asset URL, эндпоинты, коды ошибок |
| [docs/PROCESSING.md](docs/PROCESSING.md) | Операции обработки, ватермарки, ориентация, детекция |
| [docs/STORAGE.md](docs/STORAGE.md) | Хранилища fs/S3/SFTP/FTP/FTPS/HTTP, метаданные, janitor |
| [docs/SECURITY.md](docs/SECURITY.md) | Политики, лимиты, admission control, защита движков |
| [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md) | Production-развёртывание, observability, CI |

## Архитектура

```
imager.go             публичный фасад (package imager): NewServer / New(Options)
bootstrap/            переиспользуемая composition-root логика (процессоры, slog)
cmd/imager            тонкая обёртка над фасадом: env → NewServer → Run
domain/asset          грамматика и парсинг канонических URL, пресеты
domain/policy         deny-by-default политика авторизации и лимитов
domain/processing     план обработки: операции, форматы, ватермарка, ориентация
app/generatev2        use case генерации: политика → кэш → singleflight → обработка → publish
ports/                абстрактные порты (storage, processor, detector, coordinator, buffer, metadata)
adapters/httpapi      HTTP-обработчик, mux, health, admission, fallback
adapters/storage      fs, s3, sftp, ftp, http, remote-инфраструктура
adapters/processor    libvips, imagemagick, маршрутизация, detection (ONNX)
adapters/pixel        встроенные 1x1 пиксели для not-found fallback
coordination/singleflight  keyed дедупликация запросов
observability         логи (slog JSON) и метрики (expvar/Prometheus)
config/               typed конфигурация (config.go) + YAML (setting.yaml)
```

## Лицензия и репозиторий

Модуль: `github.com/pkg-ru/imager`.
