#!/bin/sh
# trivy-scan.sh - scan a kaniko-built image tar with trivy (no Docker daemon).
#
# GitVerse CI runners cannot run containers (no unshare/mount privileges),
# so trivy is executed as a standalone binary against the docker-archive tar
# produced by kaniko-build.sh. Severity and flags match the original CI:
#   --severity HIGH,CRITICAL --ignore-unfixed
#
# Usage:
#   sh docker/trivy-scan.sh <image-tar>
#
# Environment:
#   TRIVY_VERSION - trivy version (default v0.74.0)
#   TRIVY_CACHE_DIR - trivy cache dir (default /tmp/trivy-cache)

set -eu

TRIVY_VERSION="${TRIVY_VERSION:-v0.74.0}"
TRIVY_CACHE_DIR="${TRIVY_CACHE_DIR:-/tmp/trivy-cache}"

TAR="${1:?usage: trivy-scan.sh <image-tar>}"

# --- Locate or download trivy ----------------------------------------------
if command -v trivy >/dev/null 2>&1; then
    TRIVY="trivy"
else
    TRIVY="/tmp/trivy"
    if [ ! -x "$TRIVY" ]; then
        echo "[imager] downloading trivy ${TRIVY_VERSION}"
        curl -fsSL --retry 3 --connect-timeout 15 --max-time 300 \
            -o /tmp/trivy.tar.gz \
            "https://github.com/aquasecurity/trivy/releases/download/${TRIVY_VERSION}/trivy_${TRIVY_VERSION#v}_Linux-64bit.tar.gz" \
            || { echo "[imager] failed to download trivy" >&2; exit 1; }
        mkdir -p /tmp/trivy-extract
        tar -xzf /tmp/trivy.tar.gz -C /tmp/trivy-extract
        TRIVY="/tmp/trivy-extract/trivy"
        chmod +x "$TRIVY"
    fi
fi

mkdir -p "$TRIVY_CACHE_DIR"

echo "[imager] trivy scan: $TAR (severity HIGH,CRITICAL, --ignore-unfixed)"
# shellcheck disable=SC2086
"$TRIVY" image \
    --cache-dir "$TRIVY_CACHE_DIR" \
    --severity HIGH,CRITICAL \
    --ignore-unfixed \
    --input "$TAR"