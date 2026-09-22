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
        camoufox-cli \
        uv \
        requests \
        pillow \
        beautifulsoup4 \
        lxml

# Pull Camoufox (anti-detect Firefox) + the system libs it needs to run
# headless. `--with-deps` shells out to apt for the Firefox runtime
# dependencies, so this has to happen before we wipe apt lists below.
# We deliberately let this fail the build if it errors — a silently
# broken browser ships an image that looks fine until the first
# `camoufox-cli open` blows up at runtime, which is worse than a
# build-time failure with an actionable error message.
RUN camoufox-cli install --with-deps

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
