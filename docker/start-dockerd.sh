#!/bin/sh
# start-dockerd.sh - ensure a working Docker daemon on CI runners.
#
# GitVerse CI runners may ship the docker CLI without a running daemon
# (no /var/run/docker.sock, and sometimes no dockerd binary at all),
# unlike GitHub Actions where dockerd is pre-started. This script ensures
# a usable daemon with a cascade of fallbacks:
#   0. `docker info` succeeds -> done (daemon reachable via DOCKER_HOST
#      or a non-standard socket; idempotent);
#   1. stale socket cleanup: if the socket exists but the daemon is dead,
#      remove it and continue;
#   2. systemctl start docker (systemd runners, root or passwordless sudo);
#   3. service docker start (SysV fallback, Debian/Ubuntu);
#   4. dockerd in the background (log to /tmp/dockerd.log), with retries
#      using container-friendly flags (--iptables=false, --storage-driver=vfs);
#   5. if dockerd is missing entirely - install it via the package manager
#      (apt-get / apk / dnf / yum), then start it;
#   6. wait for the daemon (`docker info`) with a timeout, print diagnostics.
#
# IMPORTANT (act/GitVerse runners): background processes started inside a
# step are killed when the step finishes (the runner kills the step's process
# group). dockerd is therefore launched via `setsid` (new session) so it
# survives the end of the step and stays alive for the following steps.
#
# Environment overrides:
#   DOCKER_HOST_SOCKET  - socket path to wait for (default /var/run/docker.sock)
#   DOCKERD_WAIT_SECONDS- daemon wait timeout (default 60)
#   DOCKERD_BIN         - explicit path to the dockerd binary (skips search)
#
# Pure POSIX sh (same style as docker/lib.sh).

SOCKET="${DOCKER_HOST_SOCKET:-/var/run/docker.sock}"
TIMEOUT="${DOCKERD_WAIT_SECONDS:-60}"

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

# daemon_ok: true if the docker CLI can talk to a daemon.
daemon_ok() {
    command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1
}

# wait_for_daemon: poll `docker info` until it succeeds or the timeout elapses.
# Checking the daemon (not just the socket) avoids false positives from a
# stale socket file left by a dead daemon.
wait_for_daemon() {
    _i=0
    while [ "$_i" -lt "$TIMEOUT" ]; do
        if daemon_ok; then
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
if daemon_ok; then
    echo "[imager] Docker daemon already reachable via docker CLI (DOCKER_HOST='${DOCKER_HOST:-}')"
    exit 0
fi
echo "[imager] docker CLI present but daemon not reachable, trying to start it"

# --- 1. Stale socket cleanup --------------------------------------------------
# A socket file may exist from a previous run while the daemon is dead.
# Remove it so dockerd can bind the path again.
if [ -S "$SOCKET" ]; then
    echo "[imager] socket exists but daemon is not reachable - removing stale socket"
    run_as_root rm -f "$SOCKET" 2>/dev/null || true
fi

# --- 2. systemd ---------------------------------------------------------------
if command -v systemctl >/dev/null 2>&1; then
    echo "[imager] starting docker via systemctl"
    if run_as_root systemctl start docker 2>/dev/null; then
        if wait_for_daemon; then
            echo "[imager] Docker daemon started via systemctl"
            exit 0
        fi
        echo "[imager] systemctl start docker: daemon not ready, falling back"
    else
        echo "[imager] systemctl start docker failed (rc=$?), falling back"
    fi
fi

# --- 3. SysV service ----------------------------------------------------------
if command -v service >/dev/null 2>&1; then
    echo "[imager] starting docker via service"
    if run_as_root service docker start 2>/dev/null; then
        if wait_for_daemon; then
            echo "[imager] Docker daemon started via service"
            exit 0
        fi
        echo "[imager] service docker start: daemon not ready, falling back"
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

# launch_dockerd <bin> <flags>: start dockerd detached from the step's
# process group (setsid) so it survives the end of the step.
launch_dockerd() {
    _bin="$1"
    _flags="$2"
    if command -v setsid >/dev/null 2>&1; then
        run_as_root sh -c "setsid nohup '$_bin' $_flags > /tmp/dockerd.log 2>&1 < /dev/null &"
    else
        run_as_root sh -c "nohup '$_bin' $_flags > /tmp/dockerd.log 2>&1 < /dev/null &"
    fi
}

# try_launch_dockerd <bin>: start dockerd with progressively more
# container-friendly flags. In restricted CI containers (runner itself runs
# inside a container without NET_ADMIN and without mount privileges):
#   - iptables manipulation fails -> --iptables=false --ip6tables=false;
#   - creating the docker0 bridge fails ("operation not permitted")
#     -> --bridge=none (containers/builds use --network host);
#   - overlayfs snapshot mounts fail ("operation not permitted", buildkit)
#     -> --storage-driver=vfs (no mounts; combined with --bridge=none in one
#     attempt, since the bridge failure and the mount failure have the same
#     root cause: missing container capabilities).
# Kills the previous attempt before retrying.
try_launch_dockerd() {
    _bin="$1"
    for _flags in "" "--iptables=false --ip6tables=false" \
                  "--iptables=false --ip6tables=false --bridge=none --storage-driver=vfs"; do
        echo "[imager] starting dockerd in the background ($_bin $_flags, log: /tmp/dockerd.log)"
        launch_dockerd "$_bin" "$_flags"
        if wait_for_daemon; then
            echo "[imager] Docker daemon started (dockerd)"
            exit 0
        fi
        echo "[imager] dockerd did not become ready, stopping it and retrying"
        run_as_root sh -c "pkill -f '$_bin' 2>/dev/null; sleep 1" || true
    done
    echo "[imager] dockerd failed to start, see /tmp/dockerd.log"
    return 1
}

# --- 5. Install dockerd if missing --------------------------------------------
# Some GitVerse runners ship only the docker CLI (no dockerd binary at all).
# We are root (or have passwordless sudo), so install the daemon via the
# package manager, then start it.
install_dockerd() {
    echo "[imager] dockerd binary not found, installing via package manager"
    if command -v apt-get >/dev/null 2>&1; then
        run_as_root sh -c "apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq docker.io"
        return $?
    fi
    if command -v apk >/dev/null 2>&1; then
        run_as_root sh -c "apk add --no-cache docker"
        return $?
    fi
    if command -v dnf >/dev/null 2>&1; then
        run_as_root sh -c "dnf install -y -q docker"
        return $?
    fi
    if command -v yum >/dev/null 2>&1; then
        run_as_root sh -c "yum install -y -q docker"
        return $?
    fi
    echo "[imager] no supported package manager found (apt-get/apk/dnf/yum)" >&2
    return 1
}

if _dockerd_bin=$(find_dockerd); then
    try_launch_dockerd "$_dockerd_bin"
else
    if install_dockerd; then
        if _dockerd_bin=$(find_dockerd); then
            try_launch_dockerd "$_dockerd_bin"
        else
            echo "[imager] dockerd still not found after install" >&2
        fi
    fi
fi

# --- 6. Diagnostics -----------------------------------------------------------
# Print everything needed to understand the runner environment on failure.
echo "[imager] ERROR: Docker daemon is not available" >&2
echo "[imager] --- diagnostics ---" >&2
echo "[imager] id: $(id 2>&1)" >&2
echo "[imager] PATH: $PATH" >&2
echo "[imager] DOCKER_HOST: '${DOCKER_HOST:-}'" >&2
for _c in docker dockerd systemctl service sudo setsid apt-get apk dnf yum; do
    if command -v "$_c" >/dev/null 2>&1; then
        echo "[imager] $_c: $(command -v "$_c")" >&2
    else
        echo "[imager] $_c: NOT FOUND" >&2
    fi
done
echo "[imager] docker info error:" >&2
docker info >&2 2>&1 || true
echo "[imager] dockerd processes:" >&2
ps aux 2>/dev/null | grep -i dockerd | grep -v grep >&2 || echo "[imager]   (none)" >&2
echo "[imager] /var/run contents (docker-related):" >&2
ls -la /var/run 2>/dev/null | grep -i docker >&2 || echo "[imager]   (none)" >&2
if [ -f /tmp/dockerd.log ]; then
    echo "[imager] --- /tmp/dockerd.log (tail) ---" >&2
    tail -n 40 /tmp/dockerd.log >&2 2>/dev/null || true
fi
exit 1
