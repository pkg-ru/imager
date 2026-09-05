# Установка

## Требования

| Компонент | Назначение | Обязательность |
|-----------|------------|----------------|
| Go ≥ 1.27 | Сборка из исходников | Да (для локальной сборки) |
| libvips ≥ 8.13 + заголовки (`vips-dev`) | Основной движок обработки, все форматы включая APNG | Рекомендуется |
| C-компилятор (`gcc`/`build-base`), `pkgconf`, `musl-dev` | cgo-сборка govips | Нужны при сборке с `-tags libvips` |
| Кодеки: `libheif`, `libde265`, `libjxl`, `librsvg`, `poppler`, `libraw` | HEIF/AVIF, JPEG XL, SVG, PDF, RAW в libvips | Для соответствующих форматов |
| ONNX Runtime (`libonnxruntime`) | Детекция лиц/объектов для `fc`/`oc` | Опциональна; сборка с `-tags onnx` |

## Сборка

### С libvips (основной сценарий)

```bash
go build -tags libvips -trimpath -ldflags="-s -w" -o imager ./cmd/imager
```

libvips работает in-process (govips, cgo) и покрывает все выходные форматы: JPEG, PNG, WebP, GIF, AVIF, HEIF, APNG, JPEG XL.

### Без libvips

```bash
go build -trimpath -ldflags="-s -w" -o imager ./cmd/imager
```

Такая сборка не содержит движка обработки изображений (libvips не скомпилирован)
и подходит только для разработки/CI. Для обработки изображений соберите с
`-tags libvips`.

### С детекцией лиц/объектов

```bash
go build -tags libvips,onnx -trimpath -ldflags="-s -w" -o imager ./cmd/imager
```

Требуется установленная C-библиотека ONNX Runtime и модели в секции `detection` конфигурации (см. [PROCESSING.md](PROCESSING.md#детекция-лиц-и-объектов)).

## Запуск

```bash
IMAGER_CONFIG_DIR=./config ./imager
```

Переменные окружения: `IMAGER_CONFIG_DIR` (путь к каталогу с файлами конфигурации; по умолчанию текущий каталог) и `IMAGER_S3_ACCESS_KEY`/`IMAGER_S3_SECRET_KEY` (S3-credentials; значение из YAML приоритетнее). Обязателен `setting.yaml`; остальные файлы (`setting-local.yaml`, `generate.yaml`/`generate-local.yaml`, `failback.yaml`/`failback-local.yaml`) — опциональны. Три слоя конфигурации описаны в [CONFIGURATION.md](CONFIGURATION.md#загрузка-конфигурации).

## Docker

### Сборка образа

```bash
docker build -t imager:production .
```

Образ двухэтапный:

- **builder**: `golang:1.27.0-alpine3.23` + `build-base`, `vips-dev ~=8.17`, `libheif-dev`, `libjxl-dev`, `librsvg-dev`, `poppler-dev`, `libraw-dev`, `onnxruntime` (edge); бинарный файл собирается с `-tags libvips,onnx`;
- **runtime**: `alpine:3.23` + `libvips`, `libheif`, `libde265`, `libjxl`, `poppler-utils`, `libraw`, `librsvg`, `ghostscript`, `ffmpeg`, `onnxruntime`; non-root пользователь `imager` (uid 10001); бинарный файл `/usr/local/bin/imager`, каталог конфигурации `/etc/imager` (в compose монтируется `./config` и `./models`, см. [DEPLOYMENT.md](DEPLOYMENT.md#запуск)).

HEALTHCHECK образа опрашивает `http://127.0.0.1:8080/healthz`.

Запуск вручную и production-hardening (capabilities, tmpfs, no-new-privileges, bind-mounts) — в [DEPLOYMENT.md](DEPLOYMENT.md#запуск).

Примечание: `--read-only` не используется — см. объяснение в [DEPLOYMENT.md](DEPLOYMENT.md#укрепление-контейнера-hardening).

## Docker Compose

```bash
docker compose up -d --build
```

[`docker-compose.yaml`](../docker-compose.yaml) реализует production-hardening (tmpfs для `/tmp`, `cap_drop: ALL`, `no-new-privileges:true`, лимиты ресурсов, health-check по `/healthz`) и bind-mounts: `./config` → `/etc/imager/config:ro`, `./models` → `/etc/imager/models:ro`, `./data/source` → `/data/source:ro`, `./data/result` → `/data/result:rw`. `read_only: true` не используется — причины и полный разбор hardening в [DEPLOYMENT.md](DEPLOYMENT.md#запуск).

## Локальная разработка

Для упрощённой локальной разработки используйте цели [`Makefile`](../Makefile) (`make install`, `make build`, `make run`, `make stop`, `make restart`). Для production используйте Docker/Compose или прямую сборку `go build -tags libvips ./cmd/imager`.

## Проверка установки

```bash
# liveness/readiness
curl http://localhost:8080/healthz
curl http://localhost:8080/readyz

# метрики
curl http://localhost:8080/metrics

# генерация ассета (файл data/source/test.jpg должен существовать)
curl -o out.webp http://localhost:8080/test-jpg/thumb.webp
```
