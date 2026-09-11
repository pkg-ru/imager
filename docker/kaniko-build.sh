#!/bin/sh
# kaniko-build.sh - build the imager image with kaniko (no Docker daemon).
#
# GitVerse CI runners run inside a container WITHOUT privileges:
#   - no NET_ADMIN  -> dockerd cannot create the docker0 bridge;
#   - no mount caps -> buildkit/legacy builder cannot mount snapshots;
#   - no unshare    -> runc cannot create namespaces for RUN steps.
# Docker-based builds are therefore impossible on these runners.
#
# kaniko builds images in userspace: it unpacks base layers into a local
# directory and executes RUN steps with chroot (no namespaces, no mounts,
# no daemon). This works inside unprivileged containers.
#
# Usage:
#   sh docker/kaniko-build.sh <context-dir> <image-tag> [--target <stage>] [--build-arg K=V ...] [--push]
#
# Environment:
#   KANIKO_VERSION - kaniko executor version (default v1.24.0)
#   KANIKO_CACHE   - set to "1" to enable layer caching (default off)
#   KANIKO_CACHE_DIR - cache directory (default /kaniko-cache)
#
# Without --push: the image is exported to <image-tag>.tar (docker-archive)
# so it can be scanned by trivy without a daemon (trivy image --input <tar>).
# With --push: the image is pushed to the registry named in <image-tag>
# (credentials from ~/.docker/config.json, created by `docker login`).

set -eu

KANIKO_VERSION="${KANIKO_VERSION:-v1.24.0}"
KANIKO_CACHE="${KANIKO_CACHE:-0}"
KANIKO_CACHE_DIR="${KANIKO_CACHE_DIR:-/kaniko-cache}"

CONTEXT="${1:?usage: kaniko-build.sh <context> <tag> [--target stage] [--build-arg K=V ...] [--push]}"
TAG="${2:?usage: kaniko-build.sh <context> <tag> [--target stage] [--build-arg K=V ...] [--push]}"
shift 2

TARGET=""
BUILD_ARGS=""
PUSH=0
while [ "$#" -gt 0 ]; do
    case "$1" in
        --target)
            TARGET="$2"
            shift 2
            ;;
        --build-arg)
            BUILD_ARGS="${BUILD_ARGS} --build-arg=$2"
            shift 2
            ;;
        --push)
            PUSH=1
            shift
            ;;
        *)
            echo "[imager] unknown kaniko-build.sh argument: $1" >&2
            exit 2
            ;;
    esac
done

# --- Locate or download the kaniko executor --------------------------------
if command -v executor >/dev/null 2>&1; then
    EXECUTOR="executor"
else
    EXECUTOR="/tmp/kaniko-executor"
    if [ ! -x "$EXECUTOR" ]; then
        echo "[imager] downloading kaniko executor ${KANIKO_VERSION}"
        curl -fsSL --retry 3 --connect-timeout 15 --max-time 300 \
            -o "$EXECUTOR" \
            "https://github.com/GoogleContainerTools/kaniko/releases/download/${KANIKO_VERSION}/kaniko-executor-${KANIKO_VERSION}-linux-amd64.tar.gz" \
            || { echo "[imager] failed to download kaniko" >&2; exit 1; }
        # The release asset is a tar.gz containing the executor binary.
        mkdir -p /tmp/kaniko-extract
        tar -xzf "$EXECUTOR" -C /tmp/kaniko-extract
        EXECUTOR="/tmp/kaniko-extract/executor"
        chmod +x "$EXECUTOR"
    fi
fi

CACHE_FLAGS=""
if [ "$KANIKO_CACHE" = "1" ]; then
    mkdir -p "$KANIKO_CACHE_DIR"
    CACHE_FLAGS="--cache=true --cache-dir=$KANIKO_CACHE_DIR"
fi

TARGET_FLAGS=""
if [ -n "$TARGET" ]; then
    TARGET_FLAGS="--target=$TARGET"
fi

PUSH_FLAGS=""
if [ "$PUSH" = "1" ]; then
    PUSH_FLAGS="--push-retry=3"
else
    PUSH_FLAGS="--no-push --tar-path=$TAG.tar"
fi

echo "[imager] kaniko build: context=$CONTEXT tag=$TAG target=${TARGET:-default} push=$PUSH"
# shellcheck disable=SC2086
"$EXECUTOR" \
    --context "dir://$CONTEXT" \
    --dockerfile "$CONTEXT/Dockerfile" \
    --destination "$TAG" \
    --single-snapshot \
    --reproducible \
    $TARGET_FLAGS \
    $BUILD_ARGS \
    $CACHE_FLAGS \
    $PUSH_FLAGS

if [ "$PUSH" = "1" ]; then
    echo "[imager] kaniko build finished, image pushed to $TAG"
else
    echo "[imager] kaniko build finished, image exported to $TAG.tar"
fi