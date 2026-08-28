# Установка

## Требования

| Компонент | Назначение | Обязательность |
|-----------|------------|----------------|
| Go ≥ 1.25 | Сборка из исходников | Да (для локальной сборки) |
| libvips ≥ 8.13 + заголовки (`vips-dev`) | Основной движок обработки, все форматы включая APNG | Рекомендуется |
| C-компилятор (`gcc`/`build-base`), `pkgconf`, `musl-dev` | cgo-сборка govips | Нужны при сборке с `-tags libvips` |
| Кодеки: `libheif`, `libde265`, `libjxl`, `librsvg`, `poppler`, `libraw` | HEIF/AVIF, JPEG XL, SVG, PDF, RAW в libvips | Для соответствующих форматов |
| ImageMagick 7 (`magick`) или 6 (`convert`) | Fallback-движок для сборок без `-tags libvips` | Опционален |
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

В такой сборке основным движком становится ImageMagick. Исполняемый файл `magick` должен быть доступен в PATH либо задан явно:

```yaml
imagemagick:
  binary: "magick"          # или абсолютный путь, например "D:/tools/ImageMagick/magick.exe"
```

### С детекцией лиц/объектов

```bash
go build -tags libvips,onnx -trimpath -ldflags="-s -w" -o imager ./cmd/imager
```

Требуется установленная C-библиотека ONNX Runtime и модели в секции `detection` конфигурации (см. [PROCESSING.md](PROCESSING.md#детекция-лиц-и-объектов)).

## Запуск

```bash
IMAGER_CONFIG_DIR=./config ./imager
```

Единственная переменная окружения — `IMAGER_CONFIG_DIR`: путь к каталогу с файлами конфигурации. Обязателен `setting.yaml`; остальные (`setting-local.yaml`, `generate.yaml`/`generate-local.yaml`, `failback.yaml`/`failback-local.yaml`) — опциональны. Если переменная не задана — используется текущий каталог.

## Docker

### Сборка образа

```bash
docker build -t imager:production .
```

Образ двухэтапный:

- **builder**: `golang:1.25.0-alpine3.21` + `build-base`, `vips-dev ~=8.15`, `libheif-dev`, `libjxl-dev`, `librsvg-dev`, `poppler-dev`, `libraw-dev`; бинарный файл собирается с `-tags libvips`;
- **runtime**: `alpine:3.21` + `libvips`, `libheif`, `libde265`, `libjxl`, `poppler-utils`, `libraw`, `librsvg`, `ghostscript`, `ffmpeg`; non-root пользователь `imager` (uid 10001); бинарный файл `/usr/local/bin/imager`, каталог конфигурации `/etc/imager` (монтируется из `./config`, см. [CONFIGURATION.md](CONFIGURATION.md)).

HEALTHCHECK образа опрашивает `http://127.0.0.1:8080/healthz`.

### Запуск вручную

```bash
docker run -d \
  --read-only \
  --tmpfs /tmp:rw,noexec,nosuid,size=64m \
  --security-opt no-new-privileges:true \
  --cap-drop ALL \
  -p 8080:8080 \
  -v /host/config:/etc/imager:ro \
  -v imager_source:/data/source \
  -v imager_result:/data/result \
  -e IMAGER_CONFIG_DIR=/etc/imager \
  imager:production
```

Writable-пути внутри контейнера — только `/data/source`, `/data/result` (volumes) и `/tmp` (tmpfs).

## Docker Compose

```bash
docker compose up -d --build
```

[`docker-compose.yaml`](../docker-compose.yaml) содержит production-hardening: `read_only: true`, tmpfs для `/tmp`, `cap_drop: ALL`, `no-new-privileges:true`, лимиты ресурсов (`cpus: 2.0`, `memory: 512M`), health-check по `/healthz`. Конфигурация монтируется из `./config` в `/etc/imager` read-only.

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
