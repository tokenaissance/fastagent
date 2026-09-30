# fastclaw-sandbox (rebuilt 2026-09-30) — runtime image for the agent's exec sandbox.
#
# Built from scratch, but PINNED to the version set that works:
#   camoufox-cli 0.7.3  → camoufox==0.4.11, playwright==1.52.0   (its own pins; recorded in labels)
# Read out of local/fastclaw-sandbox-ourbuild (the earlier from-scratch build, which opens a
# browser fine). The 0.4.0 line cannot be reproduced — its `install --with-deps` now fails
# because the remote asset it points at has moved — and an UNPINNED install resolves to
# whatever the day gives, which is how the schema/config mismatch was shipped in the first place.
#
# The cache goes where the SANDBOX looks. Measured 2026-09-30 in E2B: whoami=user,
# HOME=/home/user, and NEITHER /root/.cache/camoufox nor /home/user/.cache/camoufox existed —
# the 1.3 GB the image carried under /root was simply not there, so the CLI regenerated a
# config (>5 s, wrong keys) instead of using the baked one. Moving it in the SAME layer keeps
# one copy (a `mv` in the layer that created it does not duplicate 1.3 GB) and makes it
# readable by the runtime identity.

FROM node:22-bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive \
    PIP_NO_CACHE_DIR=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
        python3 python3-pip python3-venv ca-certificates curl git sudo \
        fonts-noto-cjk fonts-noto-color-emoji \
 && ln -sf /usr/bin/python3 /usr/local/bin/python

RUN pip install --break-system-packages --no-cache-dir \
        camoufox-cli==0.7.3 \
        uv==0.11.13 \
        requests==2.33.1 \
        pillow==12.3.0 \
        beautifulsoup4==4.14.3 \
        lxml==6.1.0

# Install as root (apt needs it), then hand the cache to the runtime user in the same layer.
RUN camoufox-cli install --with-deps \
 && mkdir -p /home/user/.cache \
 && mv /root/.cache/camoufox /home/user/.cache/camoufox \
 && chown -R 1000:1000 /home/user/.cache

RUN rm -rf /var/lib/apt/lists/*

LABEL org.fastagent.camoufox-cli="0.7.3" \
      org.fastagent.camoufox="0.4.11" \
      org.fastagent.playwright="1.52.0" \
      org.fastagent.sandbox-origin="from-scratch, pinned 2026-09-30"

# Hard gate, run with the same HOME the sandbox will have: an image whose browser cannot start
# must fail HERE, not in a user's session.
RUN HOME=/home/user camoufox-cli open about:blank \
 && pip show camoufox-cli camoufox playwright | grep -E '^(Name|Version)'

WORKDIR /workspace
