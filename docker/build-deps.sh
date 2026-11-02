#!/bin/sh
# build-deps.sh - single source of truth for Alpine apk package lists.
#
# Used by Dockerfile (builder/runtime stages) and the CI image
# (.gitverse/docker/imager-ci/Dockerfile).
#
# Usage:
#   docker/build-deps.sh install-builder      # builder-stage (dev packages)
#   docker/build-deps.sh install-runtime      # runtime-stage (runtime packages)
#   docker/build-deps.sh install-edge         # edge packages (CVE fixes)
#   docker/build-deps.sh print-builder        # echo builder package names
#   docker/build-deps.sh print-runtime        # echo runtime package names
#
# Requires Alpine Linux with /etc/apk. Edge-фиксы CVE (EDGE_PACKAGES) требуют
# edge-репозиториев: они должны быть добавлены в /etc/apk/repositories ДО
# вызова install-runtime / install-edge.
# set -e критично: без него провал apk add (например, конфликт version
# constraint) не прерывал сборку — образ собирался «успешно», но без
# libvips/libheif, и бинарь падал при старте (Error loading shared library).
set -eu

# apk_add_retry — apk add с повторами. Зеркала и CDN периодически отдают
# обрезанные .apk («unexpected end of file», «failed to extract ... I/O error»),
# из-за чего сборка падала на ровном месте. apk транзакционен: неудачный
# add откатывается, поэтому повтор безопасен.
apk_add_retry() {
    _attempt=1
    while :; do
        if apk add --no-cache "$@"; then
            return 0
        fi
        if [ "$_attempt" -ge 3 ]; then
            return 1
        fi
        _attempt=$((_attempt + 1))
        echo "apk add failed, retry ${_attempt}/3 in 5s..." >&2
        sleep 5
    done
}

# Builder-stage dev packages (golang:1.27.3-alpine3.24 builder).
BUILDER_PACKAGES="build-base pkgconf musl-dev vips-dev~=8.18 glib-dev libheif-dev libde265-dev~1 libjxl-dev librsvg-dev poppler-dev libraw-dev tzdata~=2026"

# Runtime-stage packages (alpine:3.24 runtime). onnxruntime теперь в стабильном
# community-репозитории 3.24 — edge-репозиторий для него не нужен.
# ghostscript: ~10 (major pin), а не ~=10.07. В 3.24/community вышел 10.08.0,
# а ~=10.07 — minor pin (>=10.07.0,<10.08.0) → "breaks: world[ghostscript~10.07]"
# и установка runtime-пакетов падала целиком (образ оставался без vips/libheif).
RUNTIME_PACKAGES="vips-tools~=8.18 vips~=8.18 libheif~=1.23 libde265~1 libjxl~=0.11 poppler-utils libraw~=0.22 librsvg~=2.62 ghostscript~10 ffmpeg~=8.1 tzdata~=2026 ca-certificates onnxruntime"

# Edge packages: фиксы CVE, которых ещё нет в стабильном 3.24 (ffmpeg 8.1.2-r1,
# libraw 0.22.2, libass 0.17.5, nghttp2 1.70.0). Устанавливаются
# с --upgrade после включения edge-репозиториев. ~= constraint держит апгрейд внутри
# той же линейки версий, чтобы не сломать ABI-совместимость с пакетами из 3.24
# (например, vips~=8.18 собран против libde265 1.0.x; ffmpeg 9.x — несовместим).
# libde265 умышленно НЕ в edge: в Alpine ~=1.0 — minor pin (>=1.0.0,<1.1.0),
# поэтому 1.1.x из стабильного 3.24 (CVE-апгрейд) не проходит constraint и apk
# не может выбрать пакет ("breaks: world"). Используем ~1 — major pin
# (>=1.0.0,<2.0.0): 1.1.x обратно совместим с 1.0.x (ABI-стабильный),
# проходит constraint. Оставляем libde265 только в стабильном репозитории —
# там версия 1.0.x/1.1.x, ABI-совместимая с vips~=8.18.
# cups-libs: транзитивная зависимость vips (через librsvg/poppler), CVE-2026-55453
# (HIGH, newline injection в статусе принтера) — в 3.24 фикс не выпущен,
# в edge уже 2.4.20-r1. ~=2.4 держит мажор/минор.
EDGE_PACKAGES="ffmpeg~=8.1 libraw~=0.22 libass~=0.17 nghttp2~=1.70 cups-libs~=2.4"

case "${1:-}" in
    install-builder)
        # shellcheck disable=SC2086
        apk_add_retry $BUILDER_PACKAGES
        ;;
    install-runtime)
        # shellcheck disable=SC2086
        apk_add_retry $RUNTIME_PACKAGES
        # Security: обновление OpenSSL из base image (CVE-2026-14456 и др.,
        # фикс 3.5.8-r0). apk add НЕ обновляет уже установленные пакеты,
        # поэтому libcrypto3/libssl3/openssl из alpine:3.24 остаются
        # устаревшими; --upgrade форсирует обновление до последней версии.
        apk_add_retry --upgrade openssl libcrypto3 libssl3
        # Edge-фиксы CVE устанавливаются отдельным шагом install-edge
        # после включения edge-репозиториев (см. Dockerfile: runtime-base).
        ;;
    install-edge)
        # Edge-фиксы CVE (ffmpeg 8.1.2-r1, libraw 0.22.2, libass 0.17.5,
        # nghttp2 1.70.0). ~= constraints удерживают апгрейд внутри той же
        # major-версии, чтобы не сломать ABI-совместимость с пакетами из 3.24.
        # shellcheck disable=SC2086
        apk_add_retry --upgrade $EDGE_PACKAGES
        ;;
    print-builder)
        printf '%s\n' "$BUILDER_PACKAGES"
        ;;
    print-runtime)
        printf '%s\n' "$RUNTIME_PACKAGES"
        ;;
    print-edge)
        printf '%s\n' "$EDGE_PACKAGES"
        ;;
    *)
        echo "usage: $0 install-builder|install-runtime|install-edge|print-builder|print-runtime|print-edge" >&2
        exit 2
        ;;
esac