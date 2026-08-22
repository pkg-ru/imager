# syntax=docker/dockerfile:1.7

###############################################################################
# Builder: собирает production binary из cmd/imager (новый composition root).
# Pinned base image для воспроизводимости.
#
# CGO_ENABLED=1 + libvips-dev: сборка с тэком "-tags libvips" (govips, cgo).
# Дополнительные C-инструменты: gcc (через build-base), pkgconf, musl-dev.
# Библиотеки, необходимые govips для кодирования/декодирования форматов:
#   vips-dev (сам libvips), libheif-dev (HEIF/AVIF), libjxl-dev (JPEG XL),
#   librsvg-dev (SVG), libpoppler-glib-dev (PDF), libraw-dev (RAW) — гипотетически.
###############################################################################
FROM golang:1.25.0-alpine3.20 AS builder

# Воспроизводимая сборка: фиксируем версию Go toolchain из образа.
ARG GOFLAGS="-buildvcs=false"
ENV CGO_ENABLED=1 \
    GOOS=linux \
    GOARCH=amd64 \
    GOFLAGS=${GOFLAGS}

# Устанавливаем зависимости для cgo (libvips, govips) в builder.
RUN apk add --no-cache --update \
        build-base \
        pkgconf \
        musl-dev \
        vips-dev~=8.15 \
        glib-dev \
        libheif-dev \
        libde265-dev \
        libjxl-dev \
        librsvg-dev \
        poppler-dev \
        libraw-dev \
    && apk add --no-cache tzdata~=2024a

WORKDIR /src

# Сначала копируем только модули для кэширования слоя зависимостей.
COPY go.mod go.sum ./
COPY go.work go.work.sum ./
RUN go mod download

# Копируем исходники и собираем с тэком libvips.
COPY . .
RUN go build -tags libvips -trimpath -ldflags="-s -w" -o /out/imager ./cmd/imager

###############################################################################
# Runtime: минимальный образ с libvips (основной движок), ImageMagick
# (опциональный fallback для APNG) и FFmpeg.
# Pinned base image. Non-root пользователь, read-only root layout.
###############################################################################
FROM alpine:3.20

# Pinned версии пакетов для воспроизводимости (apk --no-cache).
# libvips — основной процессор; сопутствующие библиотеки кодеков:
#   libheif (HEIF/AVIF), libde265 (HEVC), libjxl (JPEG XL),
#   poppler (PDF), libraw (RAW), librsvg (SVG), ghostscript (PDF/PS).
# ImageMagick оставлен ОПЦИОНАЛЬНО для APNG (единственный формат, который
# libvips не поддерживает). ffmpeg — пост-обработка видео (если нужна).
RUN apk add --no-cache --update \
        vips-tools~=8.15 \
        libvips~=8.15 \
        libheif~=1.17 \
        libde265~=1.0 \
        libjxl~=0.10 \
        poppler-utils \
        libraw~=0.21 \
        librsvg~=2.58 \
        ghostscript~=10.02 \
        imagemagick~=7.1.1 \
        ffmpeg~=6.1 \
        tzdata~=2024a \
        ca-certificates \
    && addgroup -S -g 10001 imager \
    && adduser -S -D -H -u 10001 -G imager imager

# Часовой пояс.
ENV TZ=Europe/Moscow

# Writable каталоги: только source/result и tmp. Root fs остаётся read-only
# (read_only: true в compose). /tmp — tmpfs в compose.
RUN mkdir -p /data/source /data/result /etc/imager \
    && chown -R imager:imager /data \
    && chmod 0750 /data /data/source /data/result

# Копируем static binary и базовый конфиг. Каталог конфигурации задаётся
# через IMAGER_CONFIG_DIR (единственная env-переменная). Локальная
# конфигурация (setting-local.yaml) монтируется в compose.
COPY --from=builder /out/imager /usr/local/bin/imager
COPY config/setting.yaml /etc/imager/setting.yaml

# Dynamic binary: копируем libvips-зависимости через ld-linux. В Alpine
# динамическая линковка разрешена; пакеты уже установлены в runtime.
# (govips-библиотеки ищутся через ldconfig автоматически.)

# Restrictive permissions: бинарь 0755, конфиг 0640 (не содержит секретов,
# но ограничиваем чтение).
RUN chmod 0755 /usr/local/bin/imager \
    && chmod 0640 /etc/imager/setting.yaml \
    && chown root:imager /etc/imager/setting.yaml

# Non-root runtime user.
USER imager:imager

# Healthcheck: liveness endpoint. wget из busybox.
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1

# Экспонируем HTTP-порт (readiness/liveness/metrics/asset).
EXPOSE 8080

# Read-only root: единственные writable пути — /data и /tmp (tmpfs в compose).
VOLUME ["/data/source", "/data/result"]

# Production entrypoint: новый composition root (cmd/imager).
# Единственная env-переменная — IMAGER_CONFIG_DIR (путь к каталогу
# с setting.yaml / setting-local.yaml). Остальное — в YAML; см.
# docs/PRODUCTION.md.
CMD ["/usr/local/bin/imager"]
