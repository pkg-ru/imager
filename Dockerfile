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
#
# ONNX Runtime (детекция лиц/объектов): сборка с тэком "onnx" + cgo-библиотека
# onnxruntime (бэкенд github.com/yalue/onnxruntime_go). Пакет onnxruntime
# 1.29.0 доступен только в edge-репозитории Alpine (musl); libstdc++/libgcc
# 15.2 из edge требуются из-за C++23-символа в onnxruntime.
###############################################################################
FROM golang:1.27.0-alpine3.23 AS builder

# Воспроизводимая сборка: фиксируем версию Go toolchain из образа.
ARG GOFLAGS="-buildvcs=false"
ENV CGO_ENABLED=1 \
    GOOS=linux \
    GOARCH=amd64 \
    GOFLAGS=${GOFLAGS}

# Разрешаем сборку с тегами: базовый — "libvips"; ONNX Runtime подключается
# через "--build-arg BUILD_TAGS=libvips,onnx" (docker-compose).
ARG BUILD_TAGS=libvips,onnx

# dl-cdn.alpinelinux.org недоступен из Docker — переопределяем репозитории
# на mirror.yandex.ru (v3.23 для golang:1.27.0-alpine3.23).
RUN echo "https://mirror.yandex.ru/mirrors/alpine/v3.23/main" > /etc/apk/repositories \
    && echo "https://mirror.yandex.ru/mirrors/alpine/v3.23/community" >> /etc/apk/repositories \
    && apk add --no-cache --update \
        build-base \
        pkgconf \
        musl-dev \
        vips-dev~=8.17 \
        glib-dev \
        libheif-dev \
        libde265-dev \
        libjxl-dev \
        librsvg-dev \
        poppler-dev \
        libraw-dev \
    && apk add --no-cache tzdata~=2026 \
    && echo "https://mirror.yandex.ru/mirrors/alpine/edge/main" >> /etc/apk/repositories \
    && echo "https://mirror.yandex.ru/mirrors/alpine/edge/community" >> /etc/apk/repositories \
    && apk add --no-cache --upgrade libstdc++ libgcc onnxruntime

WORKDIR /src

# Сначала копируем только модули для кэширования слоя зависимостей.
COPY go.mod go.sum ./
COPY go.work go.work.sum ./
# Локальный replace-модуль govips (go.mod: replace ... => ./govips) должен
# присутствовать в контейнере, иначе `go mod download` не сможет прочитать
# govips/go.mod. Копируем всю директорию сразу (она же нужна и для go build).
COPY govips/ ./govips/
RUN go mod download

# Копируем исходники и собираем с тэками.
COPY . .
RUN go build -tags "$(echo ${BUILD_TAGS} | tr ',' ' ')" -trimpath -ldflags="-s -w" -o /out/imager ./cmd/imager

###############################################################################
# Runtime: минимальный образ с libvips (единственный движок) и FFmpeg.
# libvips покрывает все форматы, включая APNG (≥ 8.13). Pinned base image.
# Non-root пользователь, read-only root layout.
###############################################################################
FROM alpine:3.23

# Pinned версии пакетов для воспроизводимости (apk --no-cache).
# libvips — основной процессор; сопутствующие библиотеки кодеков:
#   libheif (HEIF/AVIF), libde265 (HEVC), libjxl (JPEG XL),
#   poppler (PDF), libraw (RAW), librsvg (SVG), ghostscript (PDF/PS).
# ffmpeg — пост-обработка видео (если нужна).
# onnxruntime — runtime для бинаря, собранного с тэком "onnx" (детекция
# лиц/объектов). Обновление libstdc++/libgcc обязательно: edge-пакет
# собран с C++23 (символ std::__format::__locale_encoding_to_utf8).
RUN echo "https://mirror.yandex.ru/mirrors/alpine/v3.23/main" > /etc/apk/repositories \
    && echo "https://mirror.yandex.ru/mirrors/alpine/v3.23/community" >> /etc/apk/repositories \
    && apk add --no-cache --update \
        vips-tools~=8.17 \
        vips~=8.17 \
        libheif~=1.23 \
        libde265~=1.0 \
        libjxl~=0.11 \
        poppler-utils \
        libraw~=0.21 \
        librsvg~=2.61 \
        ghostscript~=10.06 \
        ffmpeg~=8.0 \
        tzdata~=2026 \
        ca-certificates \
    && echo "https://mirror.yandex.ru/mirrors/alpine/edge/main" >> /etc/apk/repositories \
    && echo "https://mirror.yandex.ru/mirrors/alpine/edge/community" >> /etc/apk/repositories \
    && apk add --no-cache --upgrade libstdc++ libgcc onnxruntime \
    && addgroup -S -g 10001 imager \
    && adduser -S -D -H -u 10001 -G imager imager

# Часовой пояс.
ENV TZ=Europe/Moscow

# Writable каталоги: только source/result и tmp. Root fs остаётся read-only
# (read_only: true в compose). /tmp — tmpfs в compose.
RUN mkdir -p /data/source /data/result /etc/imager /etc/imager/models \
    && chown -R imager:imager /data \
    && chmod 0750 /data /data/source /data/result \
    && chown root:imager /etc/imager/models \
    && chmod 0755 /etc/imager/models

# Копируем static binary и базовый конфиг. Каталог конфигурации задаётся
# через IMAGER_CONFIG_DIR (единственная env-переменная). Локальная
# конфигурация (setting-local.yaml) монтируется в compose.
COPY --from=builder /out/imager /usr/local/bin/imager
COPY config/setting.yaml /etc/imager/setting.yaml

# ONNX модели (YuNet для лиц, SSD для объектов) — копируются в образ;
# в compose дополнительно монтируется ./models для обновления без пересборки.
COPY models/face_detection_yunet_2023mar.onnx /etc/imager/models/
COPY models/ssd_mobilenet_v1_12.onnx /etc/imager/models/

# Dynamic binary: копируем libvips-зависимости через ld-linux. В Alpine
# динамическая линковка разрешена; пакеты уже установлены в runtime.
# (govips-библиотеки ищутся через ldconfig автоматически.)
# libonnxruntime грузится через dlopen (библиотека установлена выше).

# Restrictive permissions: бинарь 0755, конфиг 0640 (не содержит секретов,
# но ограничиваем чтение).
RUN chmod 0755 /usr/local/bin/imager \
    && chmod 0640 /etc/imager/setting.yaml \
    && chown root:imager /etc/imager/setting.yaml \
    && chmod 0644 /etc/imager/models/*

# Non-root runtime user.
USER imager:imager

# Healthcheck: liveness endpoint. wget из busybox.
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1

# Экспонируем HTTP-порт (readiness/liveness/metrics/asset).
EXPOSE 8080

# Writable пути: /data (source/result) и /etc/imager/models.
# /etc/imager/models объявлен VOLUME, чтобы Docker создал writable mountpoint
# (анонимный volume) в верхнем слое. Без этого bind-mount ./models поверх
# каталога, существующего в read-only нижнем слое overlayfs, падает с
# "mkdirat .../etc/imager/models: read-only file system". В compose поверх
# этого mountpoint монтируется ./models:ro.
VOLUME ["/data/source", "/data/result", "/etc/imager/models"]

# Production entrypoint: новый composition root (cmd/imager).
# Единственная env-переменная — IMAGER_CONFIG_DIR (путь к каталогу
# с setting.yaml / setting-local.yaml). Остальное — в YAML; см.
# docs/PRODUCTION.md.
CMD ["/usr/local/bin/imager"]
