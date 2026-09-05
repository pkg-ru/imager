#!/bin/sh
# download-models.sh - idempotent ONNX model downloader.
#
# Downloads YuNet (face) and SSD MobileNet (object) models into
# $IMAGER_MODELS_DIR (default /etc/imager/models) if they are missing.
# Used by entrypoint.sh at container start and can be run manually:
#   sh ./docker/download-models.sh
#
# Behavior:
#   - skips download if target file exists and is non-empty (idempotent);
#   - downloads to "<name>.tmp.<pid>" then atomically mv's into place, so
#     parallel containers never observe a half-written model;
#   - on failure prints a warning but exits 0: detection in imager is
#     optional (empty model paths disable fc/oc operations), the service
#     still starts.
#
# Overridable sources (useful for private mirrors):
#   IMAGER_MODEL_FACE_URL     - YuNet model URL (face detection)
#   IMAGER_MODEL_OBJECT_URL   - SSD MobileNet v1 model URL (object detection)
#   IMAGER_MODEL_SELFIE_URL   - test image selfie.jpg URL (downloads ONLY
#                               when set; needed for real inference tests,
#                               not for production)
#   IMAGER_MODELS_DIR         - models directory (default /etc/imager/models)
#   IMAGER_SKIP_MODELS=1      - disable download entirely (offline mode)
set -u

# Models directory.
MODELS_DIR="${IMAGER_MODELS_DIR:-/etc/imager/models}"

FACE_MODEL_FILE="face_detection_yunet_2023mar.onnx"
OBJECT_MODEL_FILE="ssd_mobilenet_v1_12.onnx"
SELFIE_FILE="selfie.jpg"

DEFAULT_FACE_URL="https://github.com/opencv/opencv_zoo/raw/main/models/face_detection_yunet/face_detection_yunet_2023mar.onnx"
DEFAULT_OBJECT_URL="https://github.com/onnx/models/raw/main/validated/vision/object_detection_segmentation/ssd-mobilenetv1/model/ssd_mobilenet_v1_12.onnx"

FACE_URL="${IMAGER_MODEL_FACE_URL:-$DEFAULT_FACE_URL}"
OBJECT_URL="${IMAGER_MODEL_OBJECT_URL:-$DEFAULT_OBJECT_URL}"
SELFIE_URL="${IMAGER_MODEL_SELFIE_URL:-}"

# Minimum acceptable file size in bytes (guard against empty responses).
MIN_SIZE=1024

# -- Helpers -------------------------------------------------------------
# Общие хелперы (log/warn/fetch) берутся из docker/lib.sh, когда он доступен
# (клонированный репозиторий / build context). При standalone-развёртывании
# (скрипт скопирован в runtime-образ без lib.sh) используются локальные
# определения — поведение идентично.
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
if [ -f "${SCRIPT_DIR}/lib.sh" ]; then
    # shellcheck source=lib.sh
    LIB_PREFIX="[models]"
    . "${SCRIPT_DIR}/lib.sh"
else
    log()  { printf '[models] %s\n' "$*"; }
    warn() { printf '[models] WARNING: %s\n' "$*" >&2; }

    # Download via curl if available (follows redirects, has retries), else
    # wget (busybox wget is present in the alpine runtime image).
    fetch() { # $1 = tmp file, $2 = url
        if command -v curl >/dev/null 2>&1; then
            curl -fsSL --retry 3 --connect-timeout 15 --max-time 600 -o "$1" "$2"
        elif command -v wget >/dev/null 2>&1; then
            wget -q -T 60 -t 3 -O "$1" "$2"
        else
            warn "no curl or wget available; cannot download '$2'"
            return 1
        fi
    }
fi

# Download a single file (idempotent, atomic).
download() { # $1 = file name, $2 = url, $3 = directory
    name="$1"; url="$2"; dir="$3"
    target="$dir/$name"
    if [ -s "$target" ]; then
        log "model $name: already present, skip"
        return 0
    fi
    mkdir -p "$dir" 2>/dev/null || true
    if [ ! -w "$dir" ]; then
        warn "directory '$dir' is not writable by uid $(id -u). " \
             "Automatic model download is not possible; face-crop/object-crop " \
             "will be disabled. Make the host directory writable for uid $(id -u) " \
             "(e.g. chmod -R a+rwX ./models)."
        return 1
    fi
    tmp="$dir/$name.tmp.$$"
    rm -f "$tmp"
    if fetch "$tmp" "$url" && [ -s "$tmp" ] && \
       [ "$(stat -c %s "$tmp" 2>/dev/null || echo 0)" -ge "$MIN_SIZE" ]; then
        mv -f "$tmp" "$target" && log "model $name: downloaded and installed"
    else
        rm -f "$tmp"
        warn "failed to download '$name' from '$url'. Face/object detection " \
             "will be unavailable (fc/oc requests return an error). Check " \
             "network or override sources with IMAGER_MODEL_*_URL."
        return 1
    fi
}

# -- Main ----------------------------------------------------------------
if [ "${IMAGER_SKIP_MODELS:-0}" = "1" ]; then
    log "IMAGER_SKIP_MODELS=1 - model download skipped"
    exit 0
fi

log "models directory: $MODELS_DIR (IMAGER_MODELS_DIR)"
log "YuNet: $FACE_URL"
log "SSD:   $OBJECT_URL"
[ -n "$SELFIE_URL" ] && log "selfie: $SELFIE_URL (tests only)"

download "$FACE_MODEL_FILE" "$FACE_URL" "$MODELS_DIR"
download "$OBJECT_MODEL_FILE" "$OBJECT_URL" "$MODELS_DIR"

# Test image is downloaded ONLY when IMAGER_MODEL_SELFIE_URL is set: production
# does not need it (see adapters/processor/detection/detector_onnx_test.go).
if [ -n "$SELFIE_URL" ]; then
    download "$SELFIE_FILE" "$SELFIE_URL" "$MODELS_DIR"
fi

exit 0