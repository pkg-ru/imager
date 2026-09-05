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

# Каталоги source/result (writable) и mountpoint /etc/imager/models. ONNX-модели
# в образ НЕ копируются: при старте контейнера они скачиваются entrypoint'ом
# в смонтированный каталог (rw), см. docker/entrypoint.sh и models/README.md.
RUN mkdir -p /data/source /data/result /etc/imager /etc/imager/models \
    && chown -R imager:imager /data \
    && chmod 0750 /data /data/source /data/result \
    && chown root:imager /etc/imager/models \
    && chmod 0755 /etc/imager/models

# Static binary, базовый конфиг и entrypoint-скрипты автоскачивания моделей.
# Модели в образ не входят (скачиваются в рантайме в rw-каталог).
COPY --from=builder /out/imager /usr/local/bin/imager
COPY setting/server.yaml /etc/imager/server.yaml
COPY docker/entrypoint.sh /usr/local/bin/entrypoint.sh
COPY docker/download-models.sh /usr/local/bin/download-models.sh

# Restrictive permissions: бинарь 0755, конфиг 0640, скрипты 0755 (копируются
# как root:root, затем chown — entrypoint'у не нужен root для скачивания в
# смонтированный каталог: uid imager должен иметь запись в ./models:rw).
RUN chmod 0755 /usr/local/bin/imager \
    && chmod 0640 /etc/imager/server.yaml \
    && chown root:imager /etc/imager/server.yaml \
    && chmod 0755 /usr/local/bin/entrypoint.sh /usr/local/bin/download-models.sh \
    && chown root:imager /usr/local/bin/entrypoint.sh /usr/local/bin/download-models.sh

USER imager:imager

HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1

EXPOSE 8080

# VOLUME для /etc/imager/models обязателен: иначе bind-mount ./models поверх
# каталога в read-only нижнем слое overlayfs падает (mkdirat ...: read-only
# file system). В compose поверх mountpoint монтируется ./models:rw — каталог
# доступен на запись, entrypoint скачивает в него модели при старте.
VOLUME ["/data/source", "/data/result", "/etc/imager/models"]

# ENTRYPOINT сначала скачивает модели (идемпотентно), затем exec'ом запускает
# imager (CMD) — соблюдается PID-1 сигнальная семантика.
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["/usr/local/bin/imager"]
