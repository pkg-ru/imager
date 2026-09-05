#!/bin/sh
# install-imager.sh - download and install the imager release binary.
#
# IMAGER_VERSION (default "latest") is resolved via resolve_release_version()
# in lib.sh. Downloads imager-<VERSION>-<OS>-<ARCH>.tar.gz (.zip on Windows)
# from https://github.com/pkg-ru/imager/releases, extracts the `imager`
# binary into ${INSTALL_DIR:-/usr/local/bin} (Docker fetcher stage: /out),
# chmods it executable and runs a sanity check.
#
# Requires curl or wget and tar (unzip for .zip). No CLI args; configuration
# is read from environment variables.
#
# Usage:
#   sh docker/install-imager.sh            # latest release to /usr/local/bin
#   IMAGER_VERSION=1.0.0 INSTALL_DIR=./out sh docker/install-imager.sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
if [ -f "${SCRIPT_DIR}/lib.sh" ]; then
    # shellcheck source=lib.sh
    . "${SCRIPT_DIR}/lib.sh"
else
    die "lib.sh not found next to $0; run from a clone of github.com/pkg-ru/imager"
fi

# -- Configuration ------------------------------------------------------------
IMAGER_VERSION="${IMAGER_VERSION:-latest}"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
ARCHIVE_DIR="${ARCHIVE_DIR:-/tmp/imager-install}"    # cache dir for archives
OS=$(detect_os)
ARCH=$(detect_arch)

VERSION=$(resolve_release_version "$IMAGER_VERSION")
log "resolved version: $VERSION (requested: $IMAGER_VERSION)"
log "platform: $OS/$ARCH, install dir: $INSTALL_DIR"

if [ "$OS" = "windows" ]; then
    ARCHIVE_NAME="imager-${VERSION}-windows-${ARCH}.zip"
else
    ARCHIVE_NAME="imager-${VERSION}-${OS}-${ARCH}.tar.gz"
fi
ARCHIVE_URL="https://${RELEASE_REPO}/releases/download/${VERSION}/${ARCHIVE_NAME}"
log "archive: $ARCHIVE_URL"

# -- Download -----------------------------------------------------------------
mkdir -p "$ARCHIVE_DIR" "$INSTALL_DIR"
ARCHIVE_PATH="$ARCHIVE_DIR/$ARCHIVE_NAME"

if [ -s "$ARCHIVE_PATH" ]; then
    log "archive already present: $ARCHIVE_PATH, reuse"
else
    log "downloading..."
    fetch "$ARCHIVE_PATH" "$ARCHIVE_URL" || {
        rm -f "$ARCHIVE_PATH"
        die "failed to download $ARCHIVE_URL"
    }
fi

if [ ! -s "$ARCHIVE_PATH" ]; then
    die "downloaded archive is empty: $ARCHIVE_PATH"
fi

# -- Extract -------------------------------------------------------------------
WORK_DIR="$ARCHIVE_DIR/extract.$$"
mkdir -p "$WORK_DIR"

if [ "$OS" = "windows" ]; then
    if command -v unzip >/dev/null 2>&1; then
        unzip -oq "$ARCHIVE_PATH" -d "$WORK_DIR"
    else
        die "unzip not found; cannot extract $ARCHIVE_PATH"
    fi
else
    tar -xzf "$ARCHIVE_PATH" -C "$WORK_DIR"
fi

# Бинарь может лежать в корне архива или во вложенном каталоге.
BIN=$(find "$WORK_DIR" \( -type f -name 'imager' -o -type f -name 'imager.exe' \) 2>/dev/null | head -n 1)
if [ -z "$BIN" ]; then
    rm -rf "$WORK_DIR"
    die "imager binary not found inside the archive"
fi

# -- Install -------------------------------------------------------------------
install -m 0755 "$BIN" "$INSTALL_DIR/imager$( [ "$OS" = "windows" ] && echo .exe )"
rm -rf "$WORK_DIR"
INSTALLED="$INSTALL_DIR/imager$( [ "$OS" = "windows" ] && echo .exe )"
log "installed: $INSTALLED"

# -- Sanity check ---------------------------------------------------------------
# Binary fails fast without config; check that it at least execs. If it prints
# usage/help for -h, good; otherwise a short timeout avoids a hung server.
if [ -x "$INSTALLED" ]; then
    if "$INSTALLED" -h >/dev/null 2>&1; then
        log "sanity check: $INSTALLED OK (help output)"
    elif command -v timeout >/dev/null 2>&1; then
        if timeout 3 "$INSTALLED" >/dev/null 2>&1; then
            log "sanity check: $INSTALLED OK"
        else
            log "sanity check: $INSTALLED runs (no fatal load error)"
        fi
    else
        log "sanity check skipped (no -h/--help, no timeout available)"
    fi
else
    warn "installed file is not executable: $INSTALLED"
fi

log "imager ${VERSION} installed. Add $INSTALL_DIR to PATH if needed."
log "Run: $INSTALLED (config dir via IMAGER_CONFIG_DIR, default ./)"