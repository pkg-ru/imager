#!/bin/bash
# install-deps-macos.sh - install imager system dependencies on macOS via Homebrew.
#
# Installs: vips (libvips + all codecs), ffmpeg, onnxruntime, pkg-config.
# Idempotent: `brew install` is a no-op for already-present formulae.
#
# Requires Homebrew (https://brew.sh). Run:
#   bash docker/install-deps-macos.sh
set -euo pipefail

log()  { printf '[imager] %s\n' "$*"; }
warn() { printf '[imager] WARNING: %s\n' "$*" >&2; }

if ! command -v brew >/dev/null 2>&1; then
    echo "[imager] ERROR: Homebrew not found. Install it first: https://brew.sh" >&2
    exit 1
fi

log "Updating Homebrew..."
brew update

log "Installing vips, ffmpeg, onnxruntime, pkg-config..."
# --formula: явные формулы; onnxruntime содержит libonnxruntime + заголовки,
# которые требуются сборке с тегом onnx.
brew install --formula vips ffmpeg onnxruntime pkg-config

log "Done."
log "Next: run docker/download-models.sh, then docker/install-imager.sh (see docs/INSTALLATION.md)."