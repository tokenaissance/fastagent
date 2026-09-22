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
# Job 2 — absorb the daemon's cold-start race. The client auto-spawns a daemon
# and waits only 5 seconds for its socket; a fresh sandbox's first launch can
# take longer, and the client then exits 1 *before sending the command*
# ("Daemon did not start within 5 seconds"). The daemon it already spawned
# keeps booting (the client starts it with start_new_session), so: if the call
# failed and no socket exists, wait for the socket and run the same argv once
# more. Two properties this leans on, both true of the client today:
#
#   - no socket ⇒ ensure_daemon never got past spawning ⇒ the command was
#     never sent, so re-running it cannot replay a click / fill / submit;
#   - the same argv ⇒ the daemon the retry attaches to is configured the way
#     the caller asked (proxy, persistent profile, locale) rather than by a
#     re-derived guess. That is the reason this lives here and not in Go:
#     whoever warms a daemon early must reproduce the caller's argv exactly,
#     and only the caller's own argv does that.
#
# The guard is coarser than the failure it targets — any failure without a
# daemon pays one bounded wait — and that is deliberate: the alternative is
# parsing the client's stderr, which means buffering it.
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

"$bin" "$@"
rc=$?

if [ "$rc" -ne 0 ] && [ ! -e "$sock" ]; then
    waited=0
    while [ "$waited" -lt "$wait_secs" ] && [ ! -e "$sock" ]; do
        sleep 1
        waited=$((waited + 1))
    done
    if [ -e "$sock" ]; then
        echo "[camoufox-cli] no daemon socket after the first attempt; the daemon it started is up now — retrying once" >&2
        "$bin" "$@"
        rc=$?
    fi
fi

exit "$rc"
