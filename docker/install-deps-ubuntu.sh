#!/bin/sh
# install-deps-ubuntu.sh - install imager system dependencies on Debian/Ubuntu.
#
# Installs libvips + codecs (HEIF/AVIF, JPEG XL, SVG, PDF, RAW), FFmpeg, build
# toolchain and ONNX Runtime (prebuilt .tgz from GitHub releases) via apt.
#
# Requires root/sudo (apt). Run:
#   sudo sh docker/install-deps-ubuntu.sh
set -eu

APT_PACKAGES="
build-essential
pkg-config
ca-certificates
curl
libvips-dev
libheif-dev
libde265-0
libjxl-dev
librsvg2-dev
libpoppler-dev
libpoppler-glib-dev
libraw-dev
ffmpeg
"

# ONNX Runtime prebuilt libraries (shared lib + headers) from GitHub releases.
ONNX_VERSION="${ONNX_VERSION:-1.20.2}"
ONNX_ARCH="$(uname -m 2>/dev/null || echo x86_64)"

log()  { printf '[imager] %s\n' "$*"; }
warn() { printf '[imager] WARNING: %s\n' "$*" >&2; }

sudo_sh() { # run a command as root (sudo if not already root)
    if [ "$(id -u)" -eq 0 ]; then
        sh -c "$*"
    else
        sudo sh -c "$*"
    fi
}

log "Updating apt index and installing packages..."
sudo_sh "apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends $APT_PACKAGES"

# -- ONNX Runtime ------------------------------------------------------------
# Установка в /usr/local: cgo-сборка с тегом onnx ищет libonnxruntime.so
# по стандартным путям (ldconfig).
if ldconfig -p 2>/dev/null | grep -q 'libonnxruntime\.so'; then
    log "ONNX Runtime already present, skip download"
else
    case "$ONNX_ARCH" in
        x86_64|amd64) ORT_TARGET="linux-x64" ;;
        aarch64|arm64) ORT_TARGET="linux-aarch64" ;;
        *) ORT_TARGET="linux-x64" ;;
    esac
    ORT_BASENAME="onnxruntime-linux-${ORT_TARGET}-${ONNX_VERSION}"
    ORT_TGZ="${ORT_BASENAME}.tgz"
    ORT_URL="https://github.com/microsoft/onnxruntime/releases/download/v${ONNX_VERSION}/${ORT_TGZ}"
    TMPDIR_ONNX="$(mktemp -d)"
    trap 'rm -rf "$TMPDIR_ONNX"' EXIT

    log "Downloading ONNX Runtime ${ONNX_VERSION} (${ORT_BASENAME})..."
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL --retry 3 --connect-timeout 15 --max-time 900 -o "$TMPDIR_ONNX/$ORT_TGZ" "$ORT_URL"
    else
        wget -q -T 60 -t 3 -O "$TMPDIR_ONNX/$ORT_TGZ" "$ORT_URL"
    fi

    log "Extracting to /usr/local (libonnxruntime.so + include/)..."
    sudo_sh "tar -xzf '$TMPDIR_ONNX/$ORT_TGZ' -C '$TMPDIR_ONNX' \
        && cp -r '$TMPDIR_ONNX/$ORT_BASENAME/lib' /usr/local/lib/ \
        && cp -r '$TMPDIR_ONNX/$ORT_BASENAME/include' /usr/local/include/ \
        && ldconfig"
    log "ONNX Runtime installed to /usr/local."
fi

log "Done. Dependencies installed."
log "Next: run docker/download-models.sh, then docker/install-imager.sh (see docs/INSTALLATION.md)."