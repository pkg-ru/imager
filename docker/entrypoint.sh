#!/bin/sh
# entrypoint.sh - container entrypoint for imager.
#
# Downloads ONNX models into $IMAGER_MODELS_DIR (via download-models.sh) if
# missing, then exec's the imager binary (CMD). Model download failure is
# non-fatal: detection is optional, the service still starts.
set -u

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

DOWNLOAD_MODELS="${SCRIPT_DIR}/download-models.sh"
if [ -x "$DOWNLOAD_MODELS" ]; then
    "$DOWNLOAD_MODELS" || warn_code=$?
    # The downloader always exits 0 (non-fatal); pass rc through anyway.
    if [ "${warn_code:-0}" -ne 0 ]; then
        printf '[entrypoint] warning: model download finished with rc=%s; ' \
            "$warn_code"
        printf 'detection may be unavailable (fc/oc requests fail)\n'
    fi
else
    printf '[entrypoint] warning: %s not found; models will not be ' \
        "$DOWNLOAD_MODELS"
    printf 'auto-downloaded (detection may be unavailable)\n' >&2
fi

# exec: imager becomes PID 1, receives signals directly.
echo "[entrypoint] starting imager: $*"
exec "$@"