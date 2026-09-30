# fastagent-sandbox — runtime image for the agent's exec sandbox.
#
# Route A (2026-09-30): the browser comes from the image that WORKS, byte for byte.
#
# What went wrong with building it from scratch: this Dockerfile used to install
# `camoufox-cli` unpinned and then run `camoufox-cli install --with-deps`. The unpinned
# build resolved camoufox-cli to 0.7.3, whose config schema no longer matches the
# properties.json the browser pipeline produces, so `camoufox-cli open` died with
# "Unknown property navigator.appCodeName in config" behind an install that looked
# fine — users saw a browser that would not start. Pinning back to the working version
# (read out of this image: camoufox-cli 0.4.0, camoufox 0.4.11, playwright 1.59.0) is not
# enough either: that CLI's `install --with-deps` now FAILS (its remote asset has
# moved), which is how this build failed today.
#
# So the working artifact is the source: its browser cache, its Firefox runtime libs and
# its fonts are exactly what the agent needs, and re-deriving them is what turned a
# working sandbox into a broken one. What this layer adds is only the gate that was
# missing — an image whose browser cannot start now fails the build.
#
# The base is private (our own registry account) and is pulled while baking E2B
# templates, same as before; `build.sh --push` needs `doctl registry login`.
# The entry point is left alone on purpose: the base already carries the shim this repo
# installs (camoufox-cli -> camouflage-cli-bin), and re-running that mv here would
# overwrite the real binary with the shim.

FROM thinkany/fastclaw-sandbox:latest

# The versions this image carries, so "which version is broken?" is answered by the image
# instead of by a session log. Read from the base on 2026-09-30.
LABEL org.fastagent.camoufox-cli="0.4.0" \
      org.fastagent.camoufox="0.4.11" \
      org.fastagent.playwright="1.59.0" \
      org.fastagent.sandbox-origin="thinkany/fastclaw-sandbox:latest"

# Hard gate: an image whose browser cannot start must fail HERE, not in a user's session.
# The old CLI has no --version, so the versions are read back with pip.
RUN camoufox-cli open about:blank \
 && pip show camoufox-cli camoufox playwright | grep -E '^(Name|Version)'

WORKDIR /workspace
