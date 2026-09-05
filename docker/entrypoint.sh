#!/bin/sh
# entrypoint.sh - container entrypoint for imager.
#
# Before exec'ing the imager binary it ensures that ONNX models are present
# in $IMAGER_MODELS_DIR (downloads them via download-models.sh if missing).
# Download failures are non-fatal: detection is optional in imager (empty
# model paths disable fc/oc), so the service still starts; see
# docker/download-models.sh and adapters/processor/detection/detector.go.
#
# Usage: Dockerfile CMD is replaced with:
#   ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
#   CMD ["/usr/local/bin/imager"]
set -u

# Resolve the directory of this script (works with /bin/sh on alpine).
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

# exec replaces the shell with the imager process so signals (SIGINT/SIGTERM)
# and stdin/stdout are handled directly by the application (PID 1 semantics).
echo "[entrypoint] starting imager: $*"
exec "$@"