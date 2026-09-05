#!/bin/sh
# build-deps.sh - single source of truth for Alpine apk package lists.
#
# Used by Dockerfile (builder/runtime stages) and Dockerfile.test.
#
# Usage:
#   docker/build-deps.sh install-builder      # builder-stage (dev packages)
#   docker/build-deps.sh install-runtime      # runtime-stage (runtime packages)
#   docker/build-deps.sh install-edge         # edge packages (onnxruntime)
#   docker/build-deps.sh print-builder        # echo builder package names
#   docker/build-deps.sh print-runtime        # echo runtime package names
#
# Requires Alpine Linux with /etc/apk. The onnxruntime group (libstdc++,
# libgcc, onnxruntime) comes from the edge repository: the edge sources must
# be appended to /etc/apk/repositories BEFORE calling install-runtime /
# install-edge.
set -u

# Builder-stage dev packages (golang:1.27.0-alpine3.23 builder).
BUILDER_PACKAGES="build-base pkgconf musl-dev vips-dev~=8.17 glib-dev libheif-dev libde265-dev libjxl-dev librsvg-dev poppler-dev libraw-dev tzdata~=2026"

# Runtime-stage packages (alpine:3.23 runtime).
RUNTIME_PACKAGES="vips-tools~=8.17 vips~=8.17 libheif~=1.23 libde265~=1.0 libjxl~=0.11 poppler-utils libraw~=0.21 librsvg~=2.61 ghostscript~=10.06 ffmpeg~=8.0 tzdata~=2026 ca-certificates"

# Edge packages (C++23 onnxruntime); installed with --upgrade after the edge
# repositories are enabled.
EDGE_PACKAGES="libstdc++ libgcc onnxruntime"

case "${1:-}" in
    install-builder)
        # shellcheck disable=SC2086
        apk add --no-cache $BUILDER_PACKAGES
        ;;
    install-runtime)
        # shellcheck disable=SC2086
        apk add --no-cache $RUNTIME_PACKAGES
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