#!/bin/sh
# start-dockerd.sh - ensure a running Docker daemon on CI runners.
#
# GitVerse CI runners may ship the docker CLI without a running daemon
# (no /var/run/docker.sock), unlike GitHub Actions where dockerd is
# pre-started. This script starts the daemon with a cascade of fallbacks:
#   1. if the socket is already present -> done (idempotent);
#   2. systemctl start docker (systemd runners, root or passwordless sudo);
#   3. dockerd in the background (log to /tmp/dockerd.log);
#   4. wait for the socket with a timeout, print diagnostics on failure.
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

# --- 1. Idempotent check: daemon already running? ---------------------------
if [ -S "$SOCKET" ]; then
    echo "[imager] Docker daemon already running ($SOCKET)"
    exit 0
fi

# --- 2. systemd / service ----------------------------------------------------
# systemctl: на некоторых раннерах юнит docker отсутствует или нет прав —
# тогда пробуем SysV-скрипт `service docker start` (Debian/Ubuntu).
if command -v systemctl >/dev/null 2>&1; then
    echo "[imager] starting docker via systemctl"
    if run_as_root systemctl start docker 2>/dev/null; then
        if wait_for_socket; then
            echo "[imager] Docker daemon started via systemctl"
            exit 0
        fi
        echo "[imager] systemctl start docker: socket not ready, falling back"
    else
        echo "[imager] systemctl start docker failed, falling back"
    fi
fi

if command -v service >/dev/null 2>&1; then
    echo "[imager] starting docker via service"
    if run_as_root service docker start 2>/dev/null; then
        if wait_for_socket; then
            echo "[imager] Docker daemon started via service"
            exit 0
        fi
        echo "[imager] service docker start: socket not ready, falling back"
    else
        echo "[imager] service docker start failed, falling back"
    fi
fi

# --- 3. dockerd in the background --------------------------------------------
# dockerd может отсутствовать в PATH (например, docker CLI установлен без
# daemon-компонента или PATH урезан в CI-контейнере) — ищем бинарник в
# типичных местах установки.
find_dockerd() {
    if command -v dockerd >/dev/null 2>&1; then
        command -v dockerd
        return 0
    fi
    for _p in /usr/bin/dockerd /usr/local/bin/dockerd /usr/sbin/dockerd \
              /sbin/dockerd /snap/bin/dockerd /opt/docker/bin/dockerd; do
        if [ -x "$_p" ]; then
            printf '%s\n' "$_p"
            return 0
        fi
    done
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
fi

# --- 4. Diagnostics ----------------------------------------------------------
echo "[imager] ERROR: Docker daemon is not available" >&2
if [ -f /tmp/dockerd.log ]; then
    echo "[imager] --- /tmp/dockerd.log (tail) ---" >&2
    tail -n 40 /tmp/dockerd.log >&2 2>/dev/null || true
fi
exit 1