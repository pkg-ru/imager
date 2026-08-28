# Imager

[![CI](https://github.com/pkg-ru/imager/actions/workflows/ci.yml/badge.svg)](https://github.com/pkg-ru/imager/actions/workflows/ci.yml)
[![License: GPL v3](https://img.shields.io/badge/License-GPLv3-blue.svg)](LICENSE)
[![Go Reference](https://pkg.go.dev/badge/github.com/pkg-ru/imager.svg)](https://pkg.go.dev/github.com/pkg-ru/imager)

Imager — сервис обработки изображений на лету: генерирует, кэширует и отдаёт
изображения по каноническим URL без предварительной генерации или этапа сборки.
Источники и результаты могут находиться в локальной файловой системе,
S3-совместимом объектном хранилище, SFTP, FTP/FTPS или читаться по HTTP.

```text
GET /photos/city-skyline-jpg/300x@2.webp
→ 200 OK (WebP, 300 px wide, DPR 2), Cache-Control: public, max-age=31536000, immutable
```

## Возможности

- **Канонические URL изображений** — детерминированные URL кодируют источник,
  преобразование, размер, DPR и формат вывода; результаты неизменяемы и
  дружественны к CDN.
- **Пресеты** — именованные преобразования (`thumb@2`), разрешённые в
  конфигурации.
- **Преобразования** — изменение размера, центральная обрезка, trim, smart-crop
  (attention-based), face-crop и object-crop через ONNX-модели детекции.
- **Форматы** — JPEG, PNG, WebP, GIF, AVIF, HEIF/HEIC, APNG, JPEG XL.
- **Анимированные изображения** — GIF/WebP/APNG с лимитами на кадры и
  длительность.
- **Водяные знаки** — настраиваемое наложение с кэшированием.
- **Политика deny-by-default** — режимы авторизации (`safe`/`unsafe`), правила
  размеров, политики путей и жёсткие лимиты (байты источника/результата,
  пиксели, кадры, длительность).
- **Бэкенды хранилища** — `fs`, `s3`, `sftp`, `ftp`/`ftps` (источник и результат
  независимо друг от друга), read-only источники `http`.
- **Наблюдаемость** — структурированное JSON-логирование, метрики Prometheus на
  `/metrics`, health-check эндпоинты (`/healthz`, `/readyz`).
- **Безопасность по умолчанию** — строгая YAML-схема (`UnmarshalStrict`),
  ограниченные тела запросов, admission control, защищённые от symlink операции
  с файлами.

## Быстрый старт

### Docker Compose (рекомендуется)

```bash
git clone https://github.com/pkg-ru/imager.git
cd imager
docker compose up -d --build
curl http://localhost:8080/healthz   # {"status":"alive"}
```

Конфигурация монтируется read-only из `./config`. О защите продакшена см.
[docs/DEPLOYMENT.md](docs/DEPLOYMENT.md).

### Сборка из исходников

Требуется **Go ≥ 1.25**. Сборка по умолчанию использует процессоры-заглушки и
подходит для разработки и CI:

```bash
go build -o imager ./cmd/imager
IMAGER_CONFIG_DIR=./config ./imager
```

Продакшен-сборка включает libvips (нужен CGO):

```bash
# Debian/Ubuntu: sudo apt-get install libvips-dev build-essential pkg-config
go build -tags libvips -trimpath -ldflags="-s -w" -o imager ./cmd/imager
```

С детекцией лиц/объектов (ONNX Runtime):

```bash
go build -tags "libvips,onnx" -trimpath -ldflags="-s -w" -o imager ./cmd/imager
```

Все варианты сборки и зависимости кодеков — в
[docs/INSTALLATION.md](docs/INSTALLATION.md).

## Требования

| Компонент | Назначение | Обязательность |
|-----------|------------|----------------|
| Go ≥ 1.25 | Сборка из исходников | Да (для локальных сборок) |
| libvips ≥ 8.13 + заголовки | Основной движок обработки (все форматы, включая APNG) | Рекомендуется |
| C-компилятор, `pkg-config` | CGO-сборка govips (`-tags libvips`) | При `-tags libvips` |
| Кодеки: libheif, libde265, libjxl, librsvg, poppler, libraw | HEIF/AVIF, JPEG XL, SVG, PDF, RAW | Для соответствующих форматов |
| ONNX Runtime (`libonnxruntime`) | Детекция лиц/объектов (преобразования `fc`/`oc`) | Опционально (`-tags onnx`) |
| ffmpeg | Извлечение кадров видео | Опционально |

## Конфигурация

Все настройки задаются в YAML; CLI-флагов и переменных окружения приложения нет,
кроме `IMAGER_CONFIG_DIR` (каталог с файлами конфигурации). Конфигурация
разделена на три слоя, каждый переопределяется файлом `-local.yaml`,
игнорируемым git:

| Слой | Файлы | Содержимое |
|------|-------|------------|
| setting | `setting.yaml` + `setting-local.yaml` | Сервер, хранилище, наблюдаемость, admin (обязательный базовый файл) |
| generate | `generate.yaml` + `generate-local.yaml` | Пресеты, политика, энкодеры, водяные знаки, детекция |
| failback | `failback.yaml` + `failback-local.yaml` | Обработка not-found, source-fallback |

Секреты хранятся в файлах `*-local.yaml` (не коммитятся). Полный справочник —
в [docs/CONFIGURATION.md](docs/CONFIGURATION.md), примеры с комментариями —
в [config/](config/).

## Структура проекта

```text
imager.go              Public library facade (NewServer/New)
cmd/imager/            Binary entry point
adapters/
  httpapi/             HTTP transport, config loading, runtime wiring
  processor/
    libvips/           libvips engine (build tag: libvips)
    detection/         ONNX face/object detection (build tag: onnx)
    routing/           Processor selection
  storage/             fs, s3, sftp, ftp/ftps, http adapters
  videoframe/ffmpeg/   Video frame extraction
app/                   Application services (generatev2, adminsvc)
domain/                Pure domain logic (asset parsing, policy, processing)
ports/                 Interface contracts between layers
coordination/          In-process singleflight
observability/         Logging, metrics, middleware
bootstrap/             Process bootstrap helpers
config/                Example configuration files
docs/                  Documentation
example/               Example files (not-found page)
```

Проект построен по архитектуре ports-and-adapters: `domain` не имеет внешних
зависимостей, `ports` определяет интерфейсы, `adapters` их реализуют. Build
tags `libvips` и `onnx` переключают реализации адаптеров; без внешних
C-зависимостей компилируются заглушки, поэтому любая комбинация собирается.

## Документация

| Документ | Содержимое |
|----------|------------|
| [docs/API.md](docs/API.md) | Формат URL изображений, эндпоинты, преобразования |
| [docs/CONFIGURATION.md](docs/CONFIGURATION.md) | Полный справочник конфигурации |
| [docs/INSTALLATION.md](docs/INSTALLATION.md) | Зависимости и инструкции по сборке |
| [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md) | Продакшен-развёртывание, защита контейнера |
| [docs/PROCESSING.md](docs/PROCESSING.md) | Конвейер обработки, форматы, водяные знаки |
| [docs/STORAGE.md](docs/STORAGE.md) | Бэкенды хранилищ и их настройки |
| [docs/SECURITY.md](docs/SECURITY.md) | Политика авторизации, лимиты, безопасность URL |
| [docs/NGINX.md](docs/NGINX.md) | Настройка reverse-proxy |

## Разработка

```bash
make install   # download and tidy modules
make test      # run all tests
make race      # run tests with the race detector
make vet       # go vet
make fmt       # gofmt
make check     # fmt + vet + test + race
make fuzz      # fuzz smoke tests
```

CI ([.github/workflows/ci.yml](.github/workflows/ci.yml)) собирает и тестирует
все комбинации build tags (`default`, `libvips`, `onnx`, `libvips,onnx`)
на Linux и Windows, запускает `go vet`, `gofmt`, `govulncheck`, fuzz smoke-тесты
и сканирование контейнера Trivy.

## Лицензия

Проект распространяется по лицензии [GNU General Public License v3.0](LICENSE).
