#!/bin/sh
# start-dockerd.sh - ensure a working Docker daemon on CI runners.
#
# GitVerse CI runners may ship the docker CLI without a running daemon
# (no /var/run/docker.sock), unlike GitHub Actions where dockerd is
# pre-started. This script ensures a usable daemon with a cascade of
# fallbacks:
#   0. `docker info` succeeds -> done (daemon reachable via DOCKER_HOST
#      or a non-standard socket; idempotent);
#   1. if the socket is already present -> done (idempotent);
#   2. systemctl start docker (systemd runners, root or passwordless sudo);
#   3. service docker start (SysV fallback, Debian/Ubuntu);
#   4. dockerd in the background (log to /tmp/dockerd.log);
#   5. wait for the socket with a timeout, print diagnostics on failure.
#
# Environment overrides:
#   DOCKER_HOST_SOCKET  - socket path to wait for (default /var/run/docker.sock)
#   DOCKERD_WAIT_SECONDS- socket wait timeout (default 30)
#   DOCKERD_BIN         - explicit path to the dockerd binary (skips search)
#
# Pure POSIX sh (same style as docker/lib.sh).

SOCKET="${DOCKER_HOST_SOCKET:-/var/run/docker.sock}"
TIMEOUT="${DOCKERD_WAIT_SECONDS:-30}"

# run_as_root <cmd...>: run as root (directly or via passwordless sudo).
run_as_root() {
    if [ "$(id -u)" = "0" ]; then
        "$@"
    elif command -v sudo >/dev/null 2>&1; then
        sudo -n "$@"
    else
        return 1
    fi
}

# wait_for_socket: poll the socket until it appears or the timeout elapses.
wait_for_socket() {
    _i=0
    while [ "$_i" -lt "$TIMEOUT" ]; do
        if [ -S "$SOCKET" ]; then
            return 0
        fi
        _i=$((_i + 1))
        sleep 1
    done
    return 1
}

# --- 0. Idempotent check: is the daemon already reachable? -------------------
# Covers DOCKER_HOST (tcp://...) and non-standard socket paths: if the CLI
# can talk to a daemon, nothing needs to be started.
if command -v docker >/dev/null 2>&1; then
    if docker info >/dev/null 2>&1; then
        echo "[imager] Docker daemon already reachable via docker CLI (DOCKER_HOST='${DOCKER_HOST:-}')"
        exit 0
    fi
    echo "[imager] docker CLI present but daemon not reachable, trying to start it"
fi

# --- 1. Idempotent check: socket already present? ----------------------------
if [ -S "$SOCKET" ]; then
    echo "[imager] Docker socket already present ($SOCKET)"
    exit 0
fi

# --- 2. systemd ---------------------------------------------------------------
if command -v systemctl >/dev/null 2>&1; then
    echo "[imager] starting docker via systemctl"
    if run_as_root systemctl start docker 2>/dev/null; then
        if wait_for_socket; then
            echo "[imager] Docker daemon started via systemctl"
            exit 0
        fi
        echo "[imager] systemctl start docker: socket not ready, falling back"
    else
        echo "[imager] systemctl start docker failed (rc=$?), falling back"
    fi
fi

# --- 3. SysV service ----------------------------------------------------------
if command -v service >/dev/null 2>&1; then
    echo "[imager] starting docker via service"
    if run_as_root service docker start 2>/dev/null; then
        if wait_for_socket; then
            echo "[imager] Docker daemon started via service"
            exit 0
        fi
        echo "[imager] service docker start: socket not ready, falling back"
    else
        echo "[imager] service docker start failed (rc=$?), falling back"
    fi
fi

# --- 4. dockerd in the background ---------------------------------------------
# dockerd may be missing from PATH (docker CLI installed without the daemon
# component, or a trimmed PATH in the CI environment) - search common install
# locations, then a bounded filesystem search.
find_dockerd() {
    if [ -n "$DOCKERD_BIN" ] && [ -x "$DOCKERD_BIN" ]; then
        printf '%s\n' "$DOCKERD_BIN"
        return 0
    fi
    if command -v dockerd >/dev/null 2>&1; then
        command -v dockerd
        return 0
    fi
    for _p in /usr/bin/dockerd /usr/local/bin/dockerd /usr/sbin/dockerd \
              /sbin/dockerd /snap/bin/dockerd /opt/docker/bin/dockerd \
              /usr/lib/docker/dockerd; do
        if [ -x "$_p" ]; then
            printf '%s\n' "$_p"
            return 0
        fi
    done
    # Bounded search: common prefixes only, depth-limited, quiet.
    _found=$(find /usr /opt /snap /root -maxdepth 4 -type f -name dockerd -perm -u+x 2>/dev/null | head -n 1)
    if [ -n "$_found" ]; then
        printf '%s\n' "$_found"
        return 0
    fi
    return 1
}

if _dockerd_bin=$(find_dockerd); then
    echo "[imager] starting dockerd in the background ($_dockerd_bin, log: /tmp/dockerd.log)"
    if run_as_root sh -c "nohup '$_dockerd_bin' > /tmp/dockerd.log 2>&1 &"; then
        if wait_for_socket; then
            echo "[imager] Docker daemon started (dockerd)"
            exit 0
        fi
        echo "[imager] dockerd did not become ready, see /tmp/dockerd.log"
    else
        echo "[imager] failed to launch dockerd (no root/sudo)"
    fi
else
    echo "[imager] dockerd binary not found in PATH or common locations"
fi

# --- 5. Diagnostics -----------------------------------------------------------
# Print everything needed to understand the runner environment on failure.
echo "[imager] ERROR: Docker daemon is not available" >&2
echo "[imager] --- diagnostics ---" >&2
echo "[imager] id: $(id 2>&1)" >&2
echo "[imager] PATH: $PATH" >&2
echo "[imager] DOCKER_HOST: '${DOCKER_HOST:-}'" >&2
for _c in docker dockerd systemctl service sudo; do
    if command -v "$_c" >/dev/null 2>&1; then
        echo "[imager] $_c: $(command -v "$_c")" >&2
    else
        echo "[imager] $_c: NOT FOUND" >&2
    fi
done
echo "[imager] /var/run contents (docker-related):" >&2
ls -la /var/run 2>/dev/null | grep -i docker >&2 || echo "[imager]   (none)" >&2
if [ -f /tmp/dockerd.log ]; then
    echo "[imager] --- /tmp/dockerd.log (tail) ---" >&2
    tail -n 40 /tmp/dockerd.log >&2 2>/dev/null || true
fi
exit 1
