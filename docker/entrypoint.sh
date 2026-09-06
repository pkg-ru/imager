#!/bin/sh
# entrypoint.sh - container entrypoint for imager.
#
# 1. Bootstraps local config overrides: for each *-local.yaml.example in
#    $IMAGER_CONFIG_DIR, copies it to *-local.yaml ONLY if the target does
#    not exist yet (never overwrites user-edited / volume-mounted configs).
#    Works in a read-only config dir too (skips with a warning).
# 2. Downloads ONNX models into $IMAGER_MODELS_DIR (via download-models.sh)
#    if missing. Model download failure is non-fatal: detection is optional,
#    the service still starts.
# 3. exec's the imager binary (CMD).
set -u

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

CONFIG_DIR="${IMAGER_CONFIG_DIR:-.}"
for example in "$CONFIG_DIR"/*-local.yaml.example; do
    [ -e "$example" ] || continue
    target="${example%.example}"
    if [ -e "$target" ]; then
        echo "[entrypoint] $target already exists, skipping (not overwritten)"
    elif cp "$example" "$target" 2>/dev/null; then
        echo "[entrypoint] created $target from $example"
    else
        # Read-only config dir (e.g. docker-compose mounts it :ro) or no
        # write permission: non-fatal, *-local.yaml files are optional.
        echo "[entrypoint] warning: cannot create $target from $example " \
            "(read-only config dir?); continuing" >&2
    fi
done

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