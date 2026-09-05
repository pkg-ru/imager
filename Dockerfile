# syntax=docker/dockerfile:1.7

###############################################################################
# Builder: собирает production binary из cmd/imager.
# CGO_ENABLED=1 + -tags libvips (govips, cgo). Кодеки: vips, heif, jxl, rsvg,
# poppler, libraw. ONNX Runtime (детекция) — -tags onnx: пакет onnxruntime
# есть только в edge-репозитории Alpine (musl); libstdc++/libgcc из edge
# требуются из-за C++23-символа в onnxruntime.
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

# Сначала модули — для кэширования слоя зависимостей. Локальный replace-модуль
# govips должен быть в контейнере до `go mod download`.
COPY go.mod go.sum ./
COPY go.work go.work.sum ./
COPY govips/ ./govips/
RUN go mod download

COPY . .
RUN go build -tags "$(echo ${BUILD_TAGS} | tr ',' ' ')" -trimpath -ldflags="-s -w" -o /out/imager ./cmd/imager

###############################################################################
# Runtime: минимальный образ с libvips (все форматы, включая APNG) и FFmpeg.
# Pinned base image. Non-root пользователь.
###############################################################################
FROM alpine:3.23

# Pinned версии пакетов для воспроизводимости. onnxruntime — runtime для
# бинаря с -tags onnx; обновление libstdc++/libgcc обязательно (edge-пакет
# собран с C++23).
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

ENV TZ=Europe/Moscow

# Writable каталоги: source/result и /etc/imager/models (root fs и /tmp —
# в compose: /tmp — tmpfs).
RUN mkdir -p /data/source /data/result /etc/imager /etc/imager/models \
    && chown -R imager:imager /data \
    && chmod 0750 /data /data/source /data/result \
    && chown root:imager /etc/imager/models \
    && chmod 0755 /etc/imager/models

# Static binary, базовый конфиг и ONNX-модели (в compose ./models монтируется
# поверх для обновления без пересборки).
COPY --from=builder /out/imager /usr/local/bin/imager
COPY config/setting.yaml /etc/imager/setting.yaml
COPY models/face_detection_yunet_2023mar.onnx /etc/imager/models/
COPY models/ssd_mobilenet_v1_12.onnx /etc/imager/models/

# Restrictive permissions: бинарь 0755, конфиг 0640, модели 0644.
RUN chmod 0755 /usr/local/bin/imager \
    && chmod 0640 /etc/imager/setting.yaml \
    && chown root:imager /etc/imager/setting.yaml \
    && chmod 0644 /etc/imager/models/*

USER imager:imager

HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1

EXPOSE 8080

# VOLUME для /etc/imager/models обязателен: иначе bind-mount ./models поверх
# каталога в read-only нижнем слое overlayfs падает (mkdirat ...: read-only
# file system). В compose поверх mountpoint монтируется ./models:ro.
VOLUME ["/data/source", "/data/result", "/etc/imager/models"]

CMD ["/usr/local/bin/imager"]
