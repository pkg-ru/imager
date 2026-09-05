#!/bin/sh
# install-host.sh - one-command host install of imager (production).
#
# Orchestrates: detect OS/arch -> install system deps -> download ONNX models
# -> install the imager release binary. Supports running from a repo clone or
# remote execution:
#
#   curl -fsSL https://raw.githubusercontent.com/pkg-ru/imager/main/docker/install-host.sh | sh
#   curl -fsSL .../install-host.sh | IMAGER_VERSION=1.0.0 sh
#
# When running via curl|bash the script is NOT inside a repo clone: it clones
# the repository (shallow) into a temp dir and runs the install steps from
# there via docker/install-deps-<os>(.ps1) etc.
#
# Env overrides:
#   IMAGER_VERSION        - release version (default "latest")
#   IMAGER_MODELS_DIR     - models directory (default /etc/imager/models)
#   INSTALL_DIR           - binary install dir (default /usr/local/bin)
#   IMAGER_SKIP_DEPS=1    - skip system dependency installation
#   IMAGER_SKIP_MODELS=1  - skip model download
#   IMAGER_RELEASE_REPO   - releases repo (default github.com/pkg-ru/imager)
set -eu

log()  { printf '[imager] %s\n' "$*"; }
warn() { printf '[imager] WARNING: %s\n' "$*" >&2; }
die()  { printf '[imager] ERROR: %s\n' "$*" >&2; exit 1; }

# Where this script is running from.
SELF="$0"

# -- Determine whether we are in a repo clone ----------------------------------
IS_CLONE=0
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$SELF")" 2>/dev/null && pwd)
if [ -f "${SCRIPT_DIR}/lib.sh" ] && [ -f "${SCRIPT_DIR}/install-imager.sh" ]; then
    IS_CLONE=1
    REPO_DIR="$SCRIPT_DIR"
    log "running from repository clone: $REPO_DIR"
else
    log "not running from a clone - cloning gitverse.ru/pkg-ru/imager..."
    TMP_REPO=$(mktemp -d)
    trap 'rm -rf "$TMP_REPO"' EXIT
    if command -v git >/dev/null 2>&1; then
        # Primary: gitverse.ru/pkg-ru/imager (git). Fallback: GitHub mirror.
        if ! git clone --depth 1 https://gitverse.ru/pkg-ru/imager.git "$TMP_REPO" 2>/dev/null; then
            log "gitverse clone failed - falling back to github.com/pkg-ru/imager..."
            git clone --depth 1 https://github.com/pkg-ru/imager.git "$TMP_REPO"
        fi
        if [ ! -f "$TMP_REPO/docker/install-host.sh" ]; then
            die "failed to clone gitverse.ru/pkg-ru/imager or github.com/pkg-ru/imager; clone the repo manually"
        fi
    else
        die "git not found; cannot clone the install scripts. Clone the repo manually and run docker/install-host.sh"
    fi
    REPO_DIR="$TMP_REPO"
    # Re-exec from the clone so relative includes work.
    exec sh "$REPO_DIR/docker/install-host.sh" "$@"
fi

# -- Detect platform ------------------------------------------------------------
# Общие хелперы (log/warn/fetch, detect_os/detect_arch) из docker/lib.sh.
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
# shellcheck source=lib.sh
. "${SCRIPT_DIR}/lib.sh"

OS=$(detect_os)
ARCH=$(detect_arch)
log "platform: $OS/$ARCH"

# -- 1. System dependencies ------------------------------------------------------
if [ "${IMAGER_SKIP_DEPS:-0}" = "1" ]; then
    log "IMAGER_SKIP_DEPS=1 - skipping dependency installation"
else
    case "$OS" in
        linux)
            if command -v apt-get >/dev/null 2>&1; then
                log "installing dependencies (apt: Debian/Ubuntu)..."
                sh "$REPO_DIR/docker/install-deps-ubuntu.sh"
            elif command -v apk >/dev/null 2>&1; then
                log "alpine detected - skipping automatic deps (user-provided apk list); install runtime pkgs from docker/build-deps.sh"
                warn "on Alpine install the runtime packages manually: see docker/build-deps.sh"
            else
                warn "no apt-get detected - install deps manually (see docs/INSTALLATION.md)"
            fi
            ;;
        darwin)
            log "installing dependencies (Homebrew)..."
            bash "$REPO_DIR/docker/install-deps-macos.sh"
            ;;
        windows)
            log "installing dependencies (winget + prebuilt)..."
            powershell -ExecutionPolicy Bypass -File "$REPO_DIR/docker/install-deps-windows.ps1"
            ;;
    esac
fi

# -- 2. ONNX models ---------------------------------------------------------------
export IMAGER_MODELS_DIR="${IMAGER_MODELS_DIR:-/etc/imager/models}"
if [ "${IMAGER_SKIP_MODELS:-0}" = "1" ]; then
    log "IMAGER_SKIP_MODELS=1 - skipping model download"
else
    if [ "$OS" = "windows" ]; then
        # download-models.sh is POSIX sh; on Windows run through a sh if available
        if command -v sh >/dev/null 2>&1; then
            sh "$REPO_DIR/docker/download-models.sh"
        else
            warn "cannot run download-models.sh on windows without a sh; set IMAGER_SKIP_MODELS=1 or download models manually"
        fi
    else
        sh "$REPO_DIR/docker/download-models.sh"
    fi
fi

# -- 3. imager release binary -------------------------------------------------------
IMAGER_VERSION="${IMAGER_VERSION:-latest}"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
log "installing imager (version=$IMAGER_VERSION, dir=$INSTALL_DIR)..."
IMAGER_VERSION="$IMAGER_VERSION" INSTALL_DIR="$INSTALL_DIR" \
    sh "$REPO_DIR/docker/install-imager.sh"

log "Installation complete."
log "Verify: imager --version (see docs/INSTALLATION.md#проверка-установки)."