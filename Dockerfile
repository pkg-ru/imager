# syntax=docker/dockerfile:1.7

ARG IMAGER_VERSION=latest

###############################################################################
# Fetcher (from-release): скачивает production binary из GitHub releases
# (github.com/pkg-ru/imager — зеркало; fallback gitverse.ru/pkg-ru/imager).
# Используется default target'ом from-release — сборка не требует исходников/
# Go toolchain: install-imager.sh резолвит версию ("latest" через GitHub API/
# git ls-remote) и кладёт бинарь в /out/imager.
###############################################################################
FROM alpine:3.23 AS release-fetcher
ARG IMAGER_VERSION=latest
# busybox wget в alpine:3.23 умеет https, но CA-сертификаты нужны для проверки
# цепочки; curl предпочтительнее для редиректов GitHub.
RUN apk add --no-cache ca-certificates curl
COPY docker/lib.sh docker/install-imager.sh /tmp/
RUN IMAGER_VERSION=${IMAGER_VERSION} INSTALL_DIR=/out sh /tmp/install-imager.sh

###############################################################################
# Builder (from-source): собирает production binary из cmd/imager.
# CGO_ENABLED=1 + -tags libvips (govips, cgo). Кодеки: vips, heif, jxl, rsvg,
# poppler, libraw. ONNX Runtime (детекция) — -tags onnx: пакет onnxruntime
# есть только в edge-репозитории Alpine (musl); libstdc++/libgcc из edge
# требуются из-за C++23-символа в onnxruntime.
###############################################################################
FROM golang:1.27.0-alpine3.23 AS source-builder

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
# на mirror.yandex.ru (v3.23 для golang:1.27.0-alpine3.23). Список пакетов —
# единый источник: docker/build-deps.sh (pinned версии сохранены).
COPY docker/build-deps.sh /tmp/build-deps.sh
RUN echo "https://mirror.yandex.ru/mirrors/alpine/v3.23/main" > /etc/apk/repositories \
    && echo "https://mirror.yandex.ru/mirrors/alpine/v3.23/community" >> /etc/apk/repositories \
    && sh /tmp/build-deps.sh install-builder \
    && echo "https://mirror.yandex.ru/mirrors/alpine/edge/main" >> /etc/apk/repositories \
    && echo "https://mirror.yandex.ru/mirrors/alpine/edge/community" >> /etc/apk/repositories \
    && sh /tmp/build-deps.sh install-edge

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
# Runtime base: минимальный образ с libvips (все форматы, включая APNG) и
# FFmpeg. Pinned base image, non-root пользователь. Пакетные списки —
# из docker/build-deps.sh (install-runtime/install-edge). Бинарь копируется
# в финальных таргетах from-release / from-source.
###############################################################################
FROM alpine:3.23 AS runtime-base

# Pinned версии пакетов для воспроизводимости. onnxruntime — runtime для
# бинаря с -tags onnx; обновление libstdc++/libgcc обязательно (edge-пакет
# собран с C++23).
COPY docker/build-deps.sh /tmp/build-deps.sh
RUN echo "https://mirror.yandex.ru/mirrors/alpine/v3.23/main" > /etc/apk/repositories \
    && echo "https://mirror.yandex.ru/mirrors/alpine/v3.23/community" >> /etc/apk/repositories \
    && echo "https://mirror.yandex.ru/mirrors/alpine/edge/main" >> /etc/apk/repositories \
    && echo "https://mirror.yandex.ru/mirrors/alpine/edge/community" >> /etc/apk/repositories \
    && sh /tmp/build-deps.sh install-runtime \
    && addgroup -S -g 10001 imager \
    && adduser -S -D -H -u 10001 -G imager imager

ENV TZ=Europe/Moscow \
    IMAGER_CONFIG_DIR=/etc/imager

# Каталоги source/result (writable) и mountpoint /etc/imager/models. ONNX-модели
# в образ НЕ копируются: при старте контейнера они скачиваются entrypoint'ом
# в смонтированный каталог (rw), см. docker/entrypoint.sh.
RUN mkdir -p /data/source /data/result /etc/imager /etc/imager/models \
    && chown -R imager:imager /data \
    && chmod 0750 /data /data/source /data/result \
    && chown root:imager /etc/imager/models \
    && chmod 0755 /etc/imager/models

# Базовый конфиг и entrypoint-скрипты автоскачивания моделей.
# Модели в образ не входят (скачиваются в рантайме в rw-каталог).
COPY setting/server.yaml /etc/imager/server.yaml
COPY docker/entrypoint.sh /usr/local/bin/entrypoint.sh
COPY docker/download-models.sh /usr/local/bin/download-models.sh

# Restrictive permissions: конфиг 0640, скрипты 0755 (копируются как
# root:root, затем chown — entrypoint'у не нужен root для скачивания в
# смонтированный каталог: uid imager должен иметь запись в ./models:rw).
RUN chmod 0640 /etc/imager/server.yaml \
    && chown root:imager /etc/imager/server.yaml \
    && chmod 0755 /usr/local/bin/entrypoint.sh /usr/local/bin/download-models.sh \
    && chown root:imager /usr/local/bin/entrypoint.sh /usr/local/bin/download-models.sh

###############################################################################
# from-release (default): production binary из GitHub releases
# (github.com/pkg-ru/imager) — fetcher + runtime.
###############################################################################
FROM runtime-base AS from-release
COPY --from=release-fetcher /out/imager /usr/local/bin/imager

# Бинарь 0755, владелец root:imager.
RUN chmod 0755 /usr/local/bin/imager \
    && chown root:imager /usr/local/bin/imager

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

###############################################################################
# from-source: production binary, собранный из исходников (source-builder).
# Сборка: docker build --target from-source -t imager:from-source .
###############################################################################
FROM runtime-base AS from-source
COPY --from=source-builder /out/imager /usr/local/bin/imager

RUN chmod 0755 /usr/local/bin/imager \
    && chown root:imager /usr/local/bin/imager

USER imager:imager

HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1

EXPOSE 8080

VOLUME ["/data/source", "/data/result", "/etc/imager/models"]

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["/usr/local/bin/imager"]
