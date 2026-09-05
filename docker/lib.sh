#!/bin/sh
# lib.sh - shared helpers for imager install/distribution scripts.
#
# Logging, fetch (curl->wget fallback), OS/arch detection and release-version
# resolution. Pure POSIX sh.

# -- Logging ----------------------------------------------------------------
LIB_PREFIX="${LIB_PREFIX:-[imager]}"

log()  { printf '%s %s\n' "$LIB_PREFIX" "$*"; }
warn() { printf '%s WARNING: %s\n' "$LIB_PREFIX" "$*" >&2; }
die()  { printf '%s ERROR: %s\n' "$LIB_PREFIX" "$*" >&2; exit 1; }

# -- Fetch ------------------------------------------------------------------
# fetch <out-file> <url>: download via curl (fallback: wget); non-zero on failure.
fetch() { # $1 = output file, $2 = url
    _out="$1"; _url="$2"
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL --retry 3 --connect-timeout 15 --max-time 600 -o "$_out" "$_url"
    elif command -v wget >/dev/null 2>&1; then
        wget -q -T 60 -t 3 -O "$_out" "$_url"
    else
        warn "no curl or wget available; cannot download '$_url'"
        return 1
    fi
}

# -- OS / arch detection ----------------------------------------------------
# detect_os: prints one of linux|darwin|windows (lowercase).
detect_os() {
    case "$(uname -s 2>/dev/null | tr '[:upper:]' '[:lower:]')" in
        linux)  echo linux ;;
        darwin) echo darwin ;;
        mingw*|msys*|cygwin*|windowsnt|*windows*) echo windows ;;
        *) echo linux ;; # fallback: most CI/container environments are linux
    esac
}

# detect_arch: prints amd64 or arm64 (normalized). On x86_64 -> amd64,
# aarch64/arm64 -> arm64, everything else -> amd64 fallback.
detect_arch() {
    case "$(uname -m 2>/dev/null | tr '[:upper:]' '[:lower:]')" in
        x86_64|amd64)   echo amd64 ;;
        aarch64|arm64)  echo arm64 ;;
        *) echo amd64 ;;
    esac
}

# -- Release version resolution ----------------------------------------------
RELEASE_REPO="${IMAGER_RELEASE_REPO:-github.com/pkg-ru/imager}"
RELEASE_GIT_URL="${IMAGER_RELEASE_GIT_URL:-https://gitverse.ru/pkg-ru/imager}"

# get_latest_release_api: query GitHub "releases/latest", print tag "v1.2.3"
# or empty string.
get_latest_release_api() {
    _api_url="https://api.github.com/repos/${RELEASE_REPO}/releases/latest"
    _tag=$(
        if command -v curl >/dev/null 2>&1; then
            curl -fsSL --retry 2 --connect-timeout 10 --max-time 60 "$_api_url" 2>/dev/null
        elif command -v wget >/dev/null 2>&1; then
            wget -q -T 30 -t 2 -O - "$_api_url" 2>/dev/null
        fi
    )
    # Extract "tag_name" without jq: first sed, fallback tr+grep.
    if command -v sed >/dev/null 2>&1; then
        _t=$(printf '%s\n' "$_tag" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)
    else
        _t=$(printf '%s\n' "$_tag" | tr ',' '\n' | grep '"tag_name"' | sed 's/.*"tag_name"[^"]*"\([^"]*\)".*/\1/' | head -n 1)
    fi
    case "$_t" in
        v[0-9]*) printf '%s\n' "$_t"; return 0 ;;
        *) return 0 ;; # empty result -> caller falls back to git ls-remote
    esac
}

# get_latest_release_git: print highest semver tag matching v[0-9]* from the
# git remote; fallback to the GitHub mirror when RELEASE_GIT_URL is unreachable.
get_latest_release_git() {
    if ! command -v git >/dev/null 2>&1; then
        return 0
    fi
    for _repo in "${RELEASE_GIT_URL}" "https://${RELEASE_REPO}"; do
        _tags=$(git ls-remote --tags --sort=-v:refname "$_repo" 2>/dev/null | \
            sed -n 's#.*refs/tags/\(v[0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*\)$#\1#p' | head -n 1)
        case "$_tags" in
            v[0-9]*)
                printf '%s\n' "$_tags"
                return 0
                ;;
        esac
    done
}

# resolve_release_version <version>: "latest" -> highest semver tag; otherwise
# prints the version unchanged (normalized with a "v" prefix if missing).
# Order: GitHub API releases/latest, fallback git ls-remote --tags, then die.
resolve_release_version() {
    _ver="$1"
    if [ -z "$_ver" ]; then
        _ver="latest"
    fi
    case "$_ver" in
        latest|"")
            _resolved=$(get_latest_release_api)
            if [ -z "$_resolved" ]; then
                _resolved=$(get_latest_release_git)
            fi
            if [ -z "$_resolved" ]; then
                die "cannot resolve latest release tag for ${RELEASE_REPO} " \
                    "(GitHub API and git ls-remote both failed)"
            fi
            printf '%s\n' "$_resolved"
            ;;
        *)
            # Accept "1.0.0" and normalize to "v1.2.3"; keep "v..." as-is.
            case "$_ver" in
                v*) printf '%s\n' "$_ver" ;;
                *)  printf 'v%s\n' "$_ver" ;;
            esac
            ;;
    esac
}
