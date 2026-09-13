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
set -u

# Builder-stage dev packages (golang:1.27.0-alpine3.24 builder).
BUILDER_PACKAGES="build-base pkgconf musl-dev vips-dev~=8.18 glib-dev libheif-dev libde265-dev libjxl-dev librsvg-dev poppler-dev libraw-dev tzdata~=2026"

# Runtime-stage packages (alpine:3.24 runtime). onnxruntime теперь в стабильном
# community-репозитории 3.24 — edge-репозиторий для него не нужен.
RUNTIME_PACKAGES="vips-tools~=8.18 vips~=8.18 libheif~=1.23 libde265~=1.0 libjxl~=0.11 poppler-utils libraw~=0.22 librsvg~=2.62 ghostscript~=10.07 ffmpeg~=8.1 tzdata~=2026 ca-certificates onnxruntime"

# Edge packages: фиксы CVE, которых ещё нет в стабильном 3.24 (ffmpeg 8.1.2-r1,
# libraw 0.22.2, libde265 1.1.2, libass 0.17.5, nghttp2 1.70.0). Устанавливаются
# с --upgrade после включения edge-репозиториев.
EDGE_PACKAGES="ffmpeg libraw libde265 libass nghttp2"

case "${1:-}" in
    install-builder)
        # shellcheck disable=SC2086
        apk add --no-cache $BUILDER_PACKAGES
        ;;
    install-runtime)
        # shellcheck disable=SC2086
        apk add --no-cache $RUNTIME_PACKAGES
        # Security: обновление OpenSSL из base image (CVE-2026-14456 и др.,
        # фикс 3.5.8-r0). apk add НЕ обновляет уже установленные пакеты,
        # поэтому libcrypto3/libssl3/openssl из alpine:3.24 остаются
        # устаревшими; --upgrade форсирует обновление до последней версии.
        apk add --no-cache --upgrade openssl libcrypto3 libssl3
        # Edge-фиксы CVE (ffmpeg 8.1.2-r1, libraw 0.22.2, libde265 1.1.2,
        # libass 0.17.5, nghttp2 1.70.0) — требуют включённых edge-репозиториев.
        # shellcheck disable=SC2086
        apk add --no-cache --upgrade $EDGE_PACKAGES
        ;;
    install-edge)
        # shellcheck disable=SC2086
        apk add --no-cache --upgrade $EDGE_PACKAGES
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