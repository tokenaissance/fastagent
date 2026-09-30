# fastagent-sandbox — runtime image for the agent's exec sandbox.
#
# Why this exists: the default `python:3.12-slim` image only has Python.
# Bundled skills (like camoufox-cli) drive a real browser, so we need
# Python + Node + a browser binary pre-installed so the agent can run
# `camoufox-cli open <url>` on the first turn with zero round-trips.
#
# Base: `node:22-bookworm-slim` — small Debian image with Node already
# wired up. We add Python on top and let camoufox-cli pull its own
# anti-detect Firefox (Camoufox) via `camoufox-cli install --with-deps`.

FROM node:22-bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive \
    PIP_NO_CACHE_DIR=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1

# Pinned to what the WORKING image carried. Read out of thinkany/fastclaw-sandbox:latest on
# 2026-09-30: camoufox-cli 0.4.0, camoufox 0.4.11, playwright 1.59.0. The unpinned build resolved
# camoufox-cli to 0.7.3, whose config schema no longer matches the properties.json the browser
# pipeline produced — `camoufox-cli open` died with "Unknown property navigator.appCodeName in
# config" while looking like a successful install. These labels are how the next question about
# versions gets answered by reading the image instead of a user's session log.
LABEL org.fastagent.camoufox-cli="0.4.0" \
      org.fastagent.camoufox="0.4.11" \
      org.fastagent.playwright="1.59.0"

# System deps: Python toolchain + the usual fetch/cert essentials +
# CJK/emoji fonts so Firefox can render real glyphs instead of tofu
# (.notdef) on Chinese/Japanese/Korean pages and emoji-laden sites.
# `sudo` is needed because camoufox-cli's `install --with-deps` shells
# out to `sudo apt-get …` to grab Firefox runtime libs even when the
# caller is already root.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
        python3 \
        python3-pip \
        python3-venv \
        ca-certificates \
        curl \
        git \
        sudo \
        fonts-noto-cjk \
        fonts-noto-color-emoji \
 && ln -sf /usr/bin/python3 /usr/local/bin/python

# camoufox-cli + uv (provides `uv` and `uvx`) + common Python utilities.
# Debian Bookworm enforces PEP 668 on the system Python;
# `--break-system-packages` is the documented escape hatch and safe
# inside a container.
RUN pip install --break-system-packages --no-cache-dir \
        camoufox-cli==0.4.0 \
        uv==0.11.13 \
        requests==2.33.1 \
        pillow==12.2.0 \
        beautifulsoup4==4.14.3 \
        lxml==6.1.0

# Pull Camoufox (anti-detect Firefox) + the system libs it needs to run
# headless. `--with-deps` shells out to apt for the Firefox runtime
# dependencies, so this has to happen before we wipe apt lists below.
# We deliberately let this fail the build if it errors — a silently
# broken browser ships an image that looks fine until the first
# `camoufox-cli open` blows up at runtime, which is worse than a
# build-time failure with an actionable error message.
RUN camoufox-cli install --with-deps

# Hard gate. An image whose browser cannot start must fail HERE, not in a user's session: the 0.7.x
# incident shipped an image that installed cleanly and died on the first `camoufox-cli open`. The
# old CLI has no `--version`, so the versions are read back with `pip show`.
RUN camoufox-cli open about:blank \
 && pip show camoufox-cli camoufox playwright | grep -E '^(Name|Version)'

RUN rm -rf /var/lib/apt/lists/*

# Move the pip-installed entry point to camoufox-cli-bin and put our shim in
# its place, so every PATH-based lookup (the only way the agent invokes it)
# flows through the shim. The shim's own header explains what it does and
# why it lives here rather than in the Go code.
COPY camoufox-cli-shim.sh /usr/local/bin/camoufox-cli-shim
RUN mv /usr/local/bin/camoufox-cli /usr/local/bin/camoufox-cli-bin \
 && mv /usr/local/bin/camoufox-cli-shim /usr/local/bin/camoufox-cli \
 && chmod 0755 /usr/local/bin/camoufox-cli

WORKDIR /workspace
