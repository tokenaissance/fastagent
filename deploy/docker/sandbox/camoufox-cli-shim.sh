#!/bin/sh
# Runtime shim in front of the sandbox's camoufox-cli. Two jobs, both of which
# have to sit between the agent and the pip-installed entry point.
#
# Job 1 — auto-inject --proxy from $HTTPS_PROXY (or its lowercase variants)
# into every call whose caller didn't pass --proxy explicitly.
# Playwright/Camoufox don't pick up HTTPS_PROXY for the browser process —
# only the --proxy flag works — so without this shim a sandbox in a
# proxy-only network (e.g. behind the GFW) silently fails with
# `NS_ERROR_NET_INTERRUPT` on the first Page.goto, even though curl /
# pip / npm in the same container work fine via the env vars.
#
# Job 2 — re-run the same argv once when the call never reached a daemon.
# Two ways that happens, and the client names both of them on stderr:
#
#   - "Daemon did not start within 5 seconds" (client's spawn_daemon): the
#     daemon it spawned is still booting. Wait for the socket, then re-run.
#   - "Failed to connect to daemon after 5 attempts: [Errno 2|111]" (client's
#     send loop, connect() itself failed): the daemon that was there is gone.
#     Re-run immediately — the retry is what spawns a fresh daemon (measured
#     2026-10-01: the platform kills a daemon the START COMMAND left behind
#     about ten seconds after the sandbox is handed over, socket and browser
#     with it; a caller-spawned daemon is untouched). This is the failure a
#     user hit on the first browser call.
#
# Only those two messages retry, and that is the whole safety argument: both
# mean no daemon ever received the command, so re-running it cannot replay a
# click / fill / submit. A live daemon's own failure reports something else
# (an [Errno 32] write, a JSON error, a non-zero command result) and is
# forwarded untouched. The same argv is what makes the retry correct for a
# daemon that IS starting: it attaches to the caller's configured daemon
# (proxy, persistent profile, locale) instead of a re-derived guess — the
# reason this lives here and not in Go.
#
# Deciding the retry from the client's own words costs one buffer: the first
# attempt's stdout/stderr go to temp files and are replayed in place. An
# earlier version avoided that by retrying on "the call failed and no socket
# exists", which is the wrong predicate — a killed daemon takes its socket
# with it, so that version waited out its whole budget for a daemon nothing
# was going to start, and (with no socket at the end) never retried at all.
#
# The Dockerfile moves the pip-installed entry point to camoufox-cli-bin and
# installs this file as /usr/local/bin/camoufox-cli, so PATH-based lookups
# (the only way the agent invokes it) flow through here transparently.
#
# CAMOUFOX_CLI_REAL_BIN and CAMOUFOX_SHIM_WAIT_SECS exist so the shim's tests
# (internal/sandbox/camoufox_cli_shim_test.go) can drive a fake client; both
# default to what the image installs.

bin="${CAMOUFOX_CLI_REAL_BIN:-/usr/local/bin/camoufox-cli-bin}"
wait_secs="${CAMOUFOX_SHIM_WAIT_SECS:-20}"

proxy="${HTTPS_PROXY:-${https_proxy:-${HTTP_PROXY:-${http_proxy:-}}}}"
if [ -n "$proxy" ]; then
    caller_passed_proxy=""
    for a in "$@"; do
        case "$a" in
            --proxy|--proxy=*) caller_passed_proxy=1 ;;
        esac
    done
    if [ -z "$caller_passed_proxy" ]; then
        set -- --proxy "$proxy" "$@"
    fi
fi

# The client's socket path, as the client computes it (camoufox_cli.cli:
# SOCKET_PREFIX + session + ".sock"), so we can tell "the daemon never came up"
# from "the command itself failed". "default" is the client's default session.
session=default
previous=""
for a in "$@"; do
    if [ "$previous" = "--session" ]; then
        session="$a"
    else
        case "$a" in
            --session=*) session="${a#--session=}" ;;
        esac
    fi
    previous="$a"
done
sock="/tmp/camoufox-cli-${session}.sock"

dir="${TMPDIR:-/tmp}"; dir="${dir%/}"
out="$dir/camoufox-cli-shim-out.$$"
err="$dir/camoufox-cli-shim-err.$$"
trap 'rm -f "$out" "$err"' 0 1 2 15

"$bin" "$@" >"$out" 2>"$err"
rc=$?
cat "$out"
cat "$err" >&2

if [ "$rc" -eq 0 ]; then
    exit 0
fi

first_err=$(cat "$err")
case "$first_err" in
    *"Daemon did not start within 5 seconds"*)
        # Its daemon is still booting. Give it the shim's budget so the retry
        # attaches to that daemon instead of racing it with a second one.
        waited=0
        while [ "$waited" -lt "$wait_secs" ] && [ ! -e "$sock" ]; do
            sleep 1
            waited=$((waited + 1))
        done
        why="its daemon had not bound a socket when the client gave up"
        ;;
    *"Failed to connect to daemon after 5 attempts"*"[Errno 2]"* | \
    *"Failed to connect to daemon after 5 attempts"*"[Errno 111]"*)
        why="the daemon it was talking to is gone"
        ;;
    *)
        # A daemon received the command and answered, or a failure we cannot
        # read. Either way: no retry.
        exit "$rc"
        ;;
esac

echo "[camoufox-cli] $why — retrying once with the same argv" >&2
"$bin" "$@"
exit $?
