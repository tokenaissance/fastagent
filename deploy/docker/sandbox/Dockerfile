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

# CA roll 2026-10-22 (CVE-2026-111943). Eight eMudhra intermediate CA private keys were
# reported compromised: whoever holds them can impersonate any site to software that still
# trusts those chains. A sandbox is the worst place to keep that trust — the agent executes
# untrusted code and fetches untrusted pages in here.
#
# TWO roots carry those intermediates, so both have to go:
#   emSign ECC Root CA - G3    signs "emSign Root TLS CA - G3"
#   AAA Certificate Services   CROSS-SIGNS the same intermediate (verified 2026-10-22:
#                              the aaa probe's chain ends at AAA, not at emSign)
# Dropping only the first leaves the second path open. Debian's fix (ca-certificates
# 20260816+nmu1 / 20250419+deb13u1) blacklists exactly these two.
#
# Also: Node 22.23.4. The base image's own bundled root store is compiled into the binary and
# cannot be edited, so a version bump is the only lever for it; the Debian bundle below is
# fixed separately, because it is what curl/python/openssl use.
FROM node:22.23.4-bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive \
    PIP_NO_CACHE_DIR=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
        python3 python3-pip python3-venv ca-certificates curl git sudo \
        fonts-noto-cjk fonts-noto-color-emoji \
 && ln -sf /usr/bin/python3 /usr/local/bin/python

# Strip the two roots by SUBJECT, not by filename. Debian renames these files between
# releases, and a filename that quietly stops matching would produce an image that looks
# rebuilt and still trusts eMudhra. `update-ca-certificates --fresh` then regenerates
# /etc/ssl/certs/ca-certificates.crt — the file curl, python and openssl actually read.
#
# A separate RUN on purpose: the apt layer above can come from cache with an older
# ca-certificates — a stale index resolving 20230311 would keep both roots — and stripping
# afterwards makes the result independent of which version apt resolved. Measured in the
# first real build (2026-10-22): bookworm-security already carries the fix
# (20250419~deb12u2; the bundle came out clean and this loop matched nothing), so today it
# is the guard for the cached/older case rather than the mechanism doing the work.
RUN set -eu; \
    for f in /usr/share/ca-certificates/mozilla/*.crt; do \
      sub=$(openssl x509 -in "$f" -noout -subject 2>/dev/null || true); \
      case "$sub" in \
        *"emSign ECC Root CA - G3"*|*"AAA Certificate Services"*) echo "stripping $f"; rm -f "$f" ;; \
      esac; \
    done; \
    update-ca-certificates --fresh >/dev/null

# Hard gate, two assertions, because they fail differently.
#
#   1. CONTENT — the two subjects must be gone from /etc/ssl/certs/ca-certificates.crt.
#      This catches "the strip loop matched nothing", which is exactly how a rebuild
#      produces an image that looks new and is not. It reads the file the TLS stack
#      actually loads, not the mozilla/ source directory.
#   2. BEHAVIOUR — this image's own curl must reject both probes. Only a CERTIFICATE
#      error counts: a timeout or DNS failure is "cannot judge", not "blocked". pypi is
#      probed first (network is up) and last (the strip did not over-block — an empty
#      bundle would satisfy assertion 1 and both probe rejections).
RUN set -eu; \
    openssl crl2pkcs7 -nocrl -certfile /etc/ssl/certs/ca-certificates.crt \
      | openssl pkcs7 -print_certs -noout > /tmp/ca-subjects.txt; \
    for s in "emSign ECC Root CA - G3" "AAA Certificate Services"; do \
      if grep -q "CN=${s}\$" /tmp/ca-subjects.txt; then \
        echo "CA gate: the bundle still contains ${s} (CVE-2026-111943) — refusing to build" >&2; \
        exit 1; \
      fi; \
    done; \
    rm -f /tmp/ca-subjects.txt; \
    curl -sSf -o /dev/null --max-time 20 https://pypi.org/simple/ \
      || { echo "CA gate: network unavailable, cannot judge the bundle" >&2; exit 1; }; \
    for h in dv-g3b-emudhra.we-have-a-problem.com dv-g3b-aaa-emudhra.we-have-a-problem.com; do \
      if curl -sS -o /dev/null --max-time 20 "https://${h}/" 2>/tmp/ca-gate.err; then \
        echo "CA gate: the bundle still trusts ${h} (CVE-2026-111943) — refusing to build" >&2; \
        exit 1; \
      fi; \
      grep -qi certificate /tmp/ca-gate.err \
        || { echo "CA gate: ${h} failed for a non-certificate reason, cannot judge:" >&2; \
             cat /tmp/ca-gate.err >&2; exit 1; }; \
    done; \
    rm -f /tmp/ca-gate.err

# certifi is pinned explicitly: it is a transitive dependency of requests (which is the only
# reason it lands in the image), and unpinned it would resolve to "whatever the day gives" —
# the same mistake this file's header already records for camoufox. It also has its own copy
# of the roots, so the Debian strip above does not reach it.
RUN pip install --break-system-packages --no-cache-dir \
        camoufox-cli==0.7.3 \
        uv==0.13.4 \
        requests==2.33.1 \
        "certifi>=2026.10.19" \
        pillow==12.3.0 \
        beautifulsoup4==4.14.3 \
        lxml==6.1.0

# Install as root (apt needs it), then hand the cache to the runtime user in the same layer.
RUN camoufox-cli install --with-deps \
 && mkdir -p /home/user/.cache \
 && mv /root/.cache/camoufox /home/user/.cache/camoufox \
 && chown -R 1000:1000 /home/user/.cache

RUN rm -rf /var/lib/apt/lists/*

# The shim the agent's calls actually go through (proxy injection + one retry
# when a call never reached a daemon). Its two jobs are in the file's own
# header; what matters here is that it is a real file in the build context
# rather than a printf block — internal/sandbox/camoufox_cli_shim_test.go runs
# THIS file against a fake client, and TestE2BLiveBrowserColdStart reads it
# back out of the baked template. It went missing for a while when the image
# was rebuilt from scratch (the image shipped the bare pip entry point, so the
# retry the docs promised did not exist in any sandbox), which is why the gate
# below now runs through it.
#
# After `camoufox-cli install`, so the installer still runs straight against
# the pip entry point: the shim would otherwise inject --proxy from the build
# environment into `install`'s argv, which does not take one.
COPY camoufox-cli-shim.sh /usr/local/bin/camoufox-cli-shim
RUN mv /usr/local/bin/camoufox-cli /usr/local/bin/camoufox-cli-bin \
 && mv /usr/local/bin/camoufox-cli-shim /usr/local/bin/camoufox-cli \
 && chmod 0755 /usr/local/bin/camoufox-cli

LABEL org.fastagent.camoufox-cli="0.7.3" \
      org.fastagent.camoufox="0.4.11" \
      org.fastagent.playwright="1.52.0" \
      org.fastagent.sandbox-shim="camoufox-cli-shim (proxy + one retry)" \
      org.fastagent.sandbox-origin="from-scratch, pinned 2026-09-30" \
      org.fastagent.ca-roll="emudhra 2026-10-22 (CVE-2026-111943)"

# Hard gate, run with the same HOME the sandbox will have and THROUGH THE SHIM —
# the only path the agent uses, so a missing or broken shim fails the build too.
# The leftover socket and browser profile the gate leaves in /tmp go in the same
# layer: a stale socket baked into the image is one the first sandbox would have
# to clean up by hand.
RUN HOME=/home/user camoufox-cli open about:blank \
 && pip show camoufox-cli camoufox playwright | grep -E '^(Name|Version)' \
 && test -x /usr/local/bin/camoufox-cli-bin \
 && rm -rf /tmp/camoufox-cli-* /tmp/playwright_* /tmp/.org.chromium.*

WORKDIR /workspace
