# syntax=docker/dockerfile:1.7
###############################################################################
# CI-образ для GitVerse-пайплайна pkg-ru/imager.
#
# Назначение: единый предварительно собранный toolchain для джоб test/quality
# (.gitverse/workflows/ci.yml). Образ НЕ содержит исходников проекта — только
# инструменты и предзагруженный GOMODCACHE (модули из go.sum), чтобы джобы
# не скачивали зависимости в каждом запуске.
#
# Состав:
#   Go        1.27.0  (golang:1.27.0-alpine3.24, официальный образ)
#   libvips   + codecs (HEIF/AVIF, JPEG XL, SVG, PDF, RAW) — builder-пакеты
#   ONNX Runtime (community 3.24) — для тегов libvips,onnx
#   ffmpeg/ffprobe — интеграционные тесты видеокадров
#   gofmt     (встроен в дистрибутив Go 1.27: /usr/local/go/bin/gofmt)
#   govulncheck (golang.org/x/vuln/cmd/govulncheck) — предустановлен
#   ONNX-модели YuNet + SSD MobileNet + selfie.jpg — в /etc/imager/models
#   GOMODCACHE — предзагруженные модули из go.sum (go mod download)
#   nodejs + git — для GitHub Actions-действий (actions/checkout@v4 и др.)
#
# Версия образа — фиксированная (immutable), не latest. Обновление образа —
# отдельное контролируемое изменение (см. README в этом каталоге).
#
# Сборка и публикация (локально, с Docker):
#   docker build -f .gitverse/docker/imager-ci/Dockerfile \
#     -t gitverse.ru/pkg-ru/imager-ci:v1 .
#   docker push gitverse.ru/pkg-ru/imager-ci:v1
###############################################################################

FROM golang:1.27.0-alpine3.24

# Воспроизводимая сборка: фиксируем версию Go toolchain из образа.
# IMAGER_MODELS_DIR — каталог ONNX-моделей: тесты реального инференса
# находят его через env (см. detector_onnx_test.go: realModelsPaths).
ENV CGO_ENABLED=1 \
    GOOS=linux \
    GOARCH=amd64 \
    GOFLAGS="-buildvcs=false" \
    GOMODCACHE=/gomodcache \
    IMAGER_MODELS_DIR=/etc/imager/models \
    # gcc 15 (Alpine 3.24) собирает cgo-объекты с PIE по умолчанию; Go-линковка
    # cgo-бинарей создаёт текстовые релокации (DT_TEXTREL) в read-only секции
    # .go.func, которые musl не может применить в PIE -> segfault при загрузке
    # тестового бинаря. -no-pie отключает PIE (стандартное исправление).
    CGO_LDFLAGS="-no-pie"

# dl-cdn.alpinelinux.org недоступен из Docker — переопределяем репозитории
# на mirror.yandex.ru (v3.24 для golang:1.27.0-alpine3.24). Список пакетов —
# единый источник: docker/build-deps.sh (pinned версии сохранены).
# Дополнительно: ffmpeg/ffprobe (интеграционные тесты видеокадров),
# curl (download-models.sh), tzdata/ca-certificates (как в Dockerfile.test).
#
# ВАЖНО (GitVerse Container Registry): лимит на размер одного слоя (blob) —
# ~300MB (ошибка 413 Request Entity Too Large при push; слой 324MB не прошёл,
# слой 295MB прошёл). Поэтому установка пакетов разбита на ОТДЕЛЬНЫЕ RUN-слои,
# каждый из которых меньше лимита.
COPY docker/build-deps.sh /tmp/build-deps.sh
RUN echo "https://mirror.yandex.ru/mirrors/alpine/v3.24/main" > /etc/apk/repositories \
    && echo "https://mirror.yandex.ru/mirrors/alpine/v3.24/community" >> /etc/apk/repositories \
    && apk add --no-cache build-base pkgconf musl-dev tzdata~=2026
RUN apk add --no-cache vips-dev~=8.18 glib-dev
RUN apk add --no-cache libheif-dev libde265-dev libjxl-dev librsvg-dev poppler-dev libraw-dev
RUN apk add --no-cache ffmpeg~=8.1 curl ca-certificates
# Node.js + git — для GitHub Actions-действий (actions/checkout@v4 и др.):
# джобы test/quality выполняются ВНУТРИ этого контейнера, а Node.js-действия
# запускаются через `node` из PATH контейнера (иначе: "exec: node: not found").
# git нужен actions/checkout для клонирования. Слой ~86MB — безопасен для
# лимита QRATOR (~256MB).
RUN apk add --no-cache git nodejs
# Edge-репозитории — для edge-фиксов CVE (см. следующий шаг).
#
# ВАЖНО: апгрейд libstdc++/libgcc (и транзитивно gcc) из edge здесь УБРАН.
# Раньше он требовался for onnxruntime из edge (C++23); теперь onnxruntime
# берётся из стабильного community 3.24. Апгрейд gcc из edge
# (15.2.0-r5 -> r9) удаляет crtbegin.o из комплекта gcc, что ломает линковку
# cgo с CGO_LDFLAGS="-no-pie" (см. ENV выше): "ld: cannot find crtbegin.o".
RUN echo "https://mirror.yandex.ru/mirrors/alpine/edge/main" >> /etc/apk/repositories \
    && echo "https://mirror.yandex.ru/mirrors/alpine/edge/community" >> /etc/apk/repositories
# onnxruntime в 3.24 доступен в стабильном community; edge-фиксы CVE
# (ffmpeg/libraw/libde265/libass/nghttp2) — как в production build-deps.sh.
# (edge-пакеты не тянут апгрейд gcc: проверено сборкой Dockerfile.test.)
RUN apk add --no-cache --upgrade onnxruntime ffmpeg libraw libde265 libass nghttp2

# --- GOMODCACHE: предзагруженные модули из go.sum ---------------------------
# Копируем манифесты и govips (локальный replace-модуль), скачиваем модули.
# Джобы test/quality монтируют исходники в /src и используют GOMODCACHE,
# поэтому go mod download в каждом запуске не нужен.
#
# ВАЖНО (GitVerse Container Registry): QRATOR-прокси ограничивает размер
# одного HTTP-запроса (~256MB). Слой go mod download целиком = 260MB и не
# проходит. Разбиваем на два RUN-слоя: golang.org (111MB) и остальные (163MB).
WORKDIR /src
COPY go.mod go.sum ./
# go.work/go.work.sum НЕ копируются: go.work содержит только `use ./` и для
# сборки не нужен (сборка идёт через go.mod с replace на ./govips), а
# go.work.sum исключён из git (.gitignore) и отсутствует в CI-чекауте.
COPY govips/ ./govips/
RUN go mod download golang.org/x/crypto golang.org/x/image golang.org/x/net golang.org/x/sys golang.org/x/term golang.org/x/text
RUN go mod download -x

# --- govulncheck: предустановлен (джоба quality) ----------------------------
# go run golang.org/x/vuln/cmd/govulncheck@latest в каждом запуске скачивает
# и компилирует инструмент (~1-2 мин). Устанавливаем один раз при сборке
# образа; в CI используется `govulncheck ./...` (см. ci.yml).
RUN go install golang.org/x/vuln/cmd/govulncheck@latest \
    && ln -sf /root/.cache/go-tools/bin/govulncheck /usr/local/bin/govulncheck

# --- ONNX-модели: предустановлены (тесты реального инференса) ---------------
# YuNet (лица) + SSD MobileNet (объекты) + selfie.jpg (тестовое изображение).
# Скачиваются один раз при сборке образа в /etc/imager/models; джоба test
# находит их через IMAGER_MODELS_DIR (см. detector_onnx_test.go).
# selfie.jpg — тестовое изображение с лицом/человеком (lena.jpg из OpenCV
# samples; YuNet находит лицо, SSD — person). Скачивается один раз при сборке.
COPY docker/download-models.sh /usr/local/bin/download-models.sh
RUN chmod 0755 /usr/local/bin/download-models.sh \
    && IMAGER_MODELS_DIR=/etc/imager/models \
       IMAGER_MODEL_SELFIE_URL="https://raw.githubusercontent.com/opencv/opencv/4.x/samples/data/lena.jpg" \
       sh /usr/local/bin/download-models.sh

# Рабочий каталог по умолчанию (checkout в GitVerse монтируется в $GITHUB_WORKSPACE).
WORKDIR /workspace