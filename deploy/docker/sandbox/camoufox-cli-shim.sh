#!/bin/sh
# Runtime shim in front of the sandbox's camoufox-cli.
#
# Job 1 — auto-inject --proxy from $HTTPS_PROXY (or its lowercase variants)
# into every call whose caller didn't pass --proxy explicitly.
# Playwright/Camoufox don't pick up HTTPS_PROXY for the browser process —
# only the --proxy flag works — so without this shim a sandbox in a
# proxy-only network (e.g. behind the GFW) silently fails with
# `NS_ERROR_NET_INTERRUPT` on the first Page.goto, even though curl /
# pip / npm in the same container work fine via the env vars.
#
# The Dockerfile moves the pip-installed entry point to camoufox-cli-bin and
# installs this file as /usr/local/bin/camoufox-cli, so PATH-based lookups
# (the only way the agent invokes it) flow through here transparently.

proxy="${HTTPS_PROXY:-${https_proxy:-${HTTP_PROXY:-${http_proxy:-}}}}"
if [ -n "$proxy" ]; then
    for a in "$@"; do
        case "$a" in
            --proxy|--proxy=*) exec /usr/local/bin/camoufox-cli-bin "$@" ;;
        esac
    done
    exec /usr/local/bin/camoufox-cli-bin --proxy "$proxy" "$@"
fi
exec /usr/local/bin/camoufox-cli-bin "$@"
