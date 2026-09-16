#!/bin/sh
# entrypoint.sh - container entrypoint for imager.
#
# 1. Bootstraps config defaults from the image defaults dir
#    ($IMAGER_DEFAULTS_DIR, default /etc/imager) into $IMAGER_CONFIG_DIR.
#
#    Base configs (server.yaml, generate.yaml, failback.yaml) are force-synced
#    when the IMAGE RELEASE CHANGES: the release id (ENV IMAGER_RELEASE, or a
#    hash of the default configs when it is unset/"dev") is compared against
#    the marker file $IMAGER_CONFIG_DIR/.imager-release written by a previous
#    start. On a mismatch (or missing marker) the base configs are atomically
#    OVERWRITTEN from the image defaults, so that a container started on a new
#    image (local rebuild OR pulled from the registry) picks up updated
#    defaults in an existing volume-mounted config dir. On the SAME image
#    (normal start/restart) nothing is overwritten: base configs are copied
#    only if the target does not exist yet. Consequences:
#      - user customizations must live in *-local.yaml (never overwritten);
#      - direct edits of server.yaml/generate.yaml/failback.yaml in the
#        config dir are LOST on image update.
#
#    *-local.yaml.example templates and *-local.yaml files are NEVER
#    overwritten — only created when missing.
#
#    Works in a read-only config dir too (skips with a warning). This allows:
#      - running without any config mounts (all defaults from the image);
#      - mounting an EMPTY ./setting dir — defaults are populated on start;
#      - overriding only *-local.yaml (base files stay from the image);
#      - overriding all configs (mount your own dir with the full set).
# 2. Downloads ONNX models into $IMAGER_MODELS_DIR (via download-models.sh)
#    if missing. Model download failure is non-fatal: detection is optional,
#    the service still starts.
# 3. exec's the imager binary (CMD).
set -u

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

CONFIG_DIR="${IMAGER_CONFIG_DIR:-.}"
DEFAULTS_DIR="${IMAGER_DEFAULTS_DIR:-/etc/imager}"

# Base configs subject to force-sync on image release change.
BASE_CONFIGS="server.yaml generate.yaml failback.yaml"
RELEASE_MARKER="$CONFIG_DIR/.imager-release"

# release_id: print the current image release id.
# IMAGER_RELEASE is set at build time (ARG/ENV, see Dockerfile). When empty
# or "dev" (e.g. a locally built image without --build-arg), fall back to a
# hash of the default configs themselves: any change to the shipped configs
# produces a different id.
release_id() {
    rid="${IMAGER_RELEASE:-}"
    if [ -z "$rid" ] || [ "$rid" = "dev" ]; then
        if command -v sha256sum >/dev/null 2>&1; then
            rid=$(sha256sum \
                "$DEFAULTS_DIR/server.yaml" \
                "$DEFAULTS_DIR/generate.yaml" \
                "$DEFAULTS_DIR/failback.yaml" 2>/dev/null | sha256sum 2>/dev/null | cut -c1-16)
        else
            rid=""
        fi
    fi
    printf '%s' "$rid"
}

# sync_base_config <src> <name>: atomically overwrite $CONFIG_DIR/<name> with
# <src> (tmp file in the config dir + mv). Runs as non-root normally: chown is
# best-effort (root:imager 0640 like in the Dockerfile) and never fatal.
sync_base_config() {
    _src="$1"; _name="$2"
    _target="$CONFIG_DIR/$_name"
    _tmp="$CONFIG_DIR/.${_name}.tmp.$$"
    if ! cp "$_src" "$_tmp" 2>/dev/null; then
        # Read-only config dir (e.g. docker-compose mounts it :ro) or no
        # write permission: non-fatal, previous configs remain in place.
        echo "[entrypoint] warning: cannot sync $_target from image " \
            "defaults (read-only config dir?); keeping existing file" >&2
        return 0
    fi
    chmod 0640 "$_tmp" 2>/dev/null || true
    chown root:imager "$_tmp" 2>/dev/null || true
    if mv -f "$_tmp" "$_target" 2>/dev/null; then
        echo "[entrypoint] synced $_target from image defaults"
    else
        rm -f "$_tmp" 2>/dev/null || true
        echo "[entrypoint] warning: cannot replace $_target from image " \
            "defaults; keeping existing file" >&2
    fi
}

# Ensure the config dir exists (e.g. IMAGER_CONFIG_DIR points to a path that
# is not mounted yet). Non-fatal if it cannot be created (read-only fs).
mkdir -p "$CONFIG_DIR" 2>/dev/null || true

if [ -d "$DEFAULTS_DIR" ] && [ "$DEFAULTS_DIR" != "$CONFIG_DIR" ]; then
    _rid=$(release_id)
    _current=""
    if [ -e "$RELEASE_MARKER" ]; then
        _current=$(cat "$RELEASE_MARKER" 2>/dev/null || true)
    fi

    if [ -n "$_rid" ] && [ "$_current" != "$_rid" ]; then
        # New image release (or first start / no marker): force-sync base
        # configs from the image defaults, overwriting existing files.
        for _name in $BASE_CONFIGS; do
            _src="$DEFAULTS_DIR/$_name"
            [ -e "$_src" ] || continue
            sync_base_config "$_src" "$_name"
        done
        # Update the marker so subsequent starts on the same image keep the
        # current behavior (copy only missing files).
        if printf '%s\n' "$_rid" > "$RELEASE_MARKER" 2>/dev/null; then
            chmod 0644 "$RELEASE_MARKER" 2>/dev/null || true
            chown root:imager "$RELEASE_MARKER" 2>/dev/null || true
            echo "[entrypoint] config defaults synced to image release $_rid"
        else
            echo "[entrypoint] warning: cannot write $RELEASE_MARKER; " \
                "defaults will be re-synced on the next start" >&2
        fi
    else
        # Same image release (or unknown id): keep legacy behavior — copy
        # only files that do not exist in the config dir yet.
        for src in "$DEFAULTS_DIR"/*.yaml; do
            [ -e "$src" ] || continue
            name=$(basename "$src")
            target="$CONFIG_DIR/$name"
            if [ -e "$target" ]; then
                # echo "[entrypoint] $target already exists, skipping (not overwritten)"
                continue
            elif cp "$src" "$target" 2>/dev/null; then
                echo "[entrypoint] created $target from image defaults"
            else
                # Read-only config dir (e.g. docker-compose mounts it :ro) or
                # no write permission: non-fatal, configs are optional.
                echo "[entrypoint] warning: cannot create $target from image " \
                    "defaults (read-only config dir?); continuing" >&2
            fi
        done
    fi

    # Bootstrap *-local.yaml.example templates: only if missing (never
    # overwritten — client-side files).
    for src in "$DEFAULTS_DIR"/*-local.yaml.example; do
        [ -e "$src" ] || continue
        name=$(basename "$src")
        target="$CONFIG_DIR/$name"
        if [ -e "$target" ]; then
            # echo "[entrypoint] $target already exists, skipping (not overwritten)"
            continue
        elif cp "$src" "$target" 2>/dev/null; then
            echo "[entrypoint] created $target from image defaults"
        else
            # Read-only config dir (e.g. docker-compose mounts it :ro) or no
            # write permission: non-fatal, configs are optional.
            echo "[entrypoint] warning: cannot create $target from image " \
                "defaults (read-only config dir?); continuing" >&2
        fi
    done
fi

# Bootstrap local config overrides: for each *-local.yaml.example in
# $IMAGER_CONFIG_DIR, copies it to *-local.yaml ONLY if the target does
# not exist yet (never overwrites user-edited / volume-mounted configs).
for example in "$CONFIG_DIR"/*-local.yaml.example; do
    [ -e "$example" ] || continue
    target="${example%.example}"
    if [ -e "$target" ]; then
        # echo "[entrypoint] $target already exists, skipping (not overwritten)"
        continue
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
# echo "[entrypoint] starting imager: $*"
exec "$@"
