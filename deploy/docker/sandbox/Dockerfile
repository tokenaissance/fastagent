# fastclaw-sandbox — runtime image for the agent's exec sandbox.
#
# Why this exists: the default `python:3.12-slim` image only has Python.
# Skills like `agent-browser` are Node CLIs that drive headless Chromium,
# so `npx` / Chromium are missing and the agent's first install attempt
# fails with `npm: not found`. This image bundles Python + Node +
# Chromium (via Playwright) so the common skill set works out of the
# box.
#
# Base: Microsoft's Playwright image already has Chromium + Firefox +
# WebKit + their system deps wired up; we just add Python on top so we
# don't lose the python:3.12-slim baseline.

FROM mcr.microsoft.com/playwright:v1.49.0-jammy

ENV DEBIAN_FRONTEND=noninteractive \
    PIP_NO_CACHE_DIR=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1

# Python 3 + pip + venv. Jammy ships python3.10 — that's fine for
# skills (the agent's own runtime is the Go binary, not Python). If a
# skill needs 3.12 specifically, override the base or add deadsnakes.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
        python3 \
        python3-pip \
        python3-venv \
        ca-certificates \
        curl \
        git \
 && ln -sf /usr/bin/python3 /usr/local/bin/python \
 && rm -rf /var/lib/apt/lists/*

# Common Python deps the python:3.12-slim users picked up by reflex.
# Skipping anything heavy (numpy/pandas) — install on demand from skills.
# Jammy's system python isn't PEP 668 externally-managed, and the
# bundled pip predates --break-system-packages, so we install plainly.
RUN pip install --no-cache-dir \
        requests \
        pillow \
        beautifulsoup4 \
        lxml

# Pre-install the agent-browser CLI globally so `agent-browser …`
# resolves immediately. Failure non-fatal: a registry hiccup at build
# time still produces a runnable image and the agent can fall back
# to `npm i -g agent-browser` at runtime.
RUN npm install -g agent-browser || true

# Make Playwright's bundled Chromium discoverable to agent-browser.
# agent-browser auto-launch only checks four fixed locations
# (its own cache, system Chrome, Puppeteer cache, ~/.cache/ms-playwright)
# and ignores PLAYWRIGHT_BROWSERS_PATH, so without this it errors with
# "Chrome not found" even though arm64 Chromium is sitting at
# /ms-playwright/chromium-*/chrome-linux/chrome. The /usr/bin/chromium-browser
# symlink trips agent-browser's "System Chrome" check, which is what
# upstream's own error message recommends for non-x86_64 hosts (Chrome
# for Testing has no Linux ARM64 build, so `agent-browser install`
# isn't an option on Apple Silicon / Graviton). Glob expands at build
# time to today's revision, so a Playwright base bump just regenerates
# the symlink on the next image build.
RUN ln -sfn "$(echo /ms-playwright/chromium-*/chrome-linux/chrome)" /usr/bin/chromium-browser

WORKDIR /workspace
