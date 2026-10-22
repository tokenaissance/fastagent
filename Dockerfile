# --- Stage 1: Build web UI ---
# Same CA roll as the sandbox (2026-10-22, CVE-2026-111943): this stage fetches packages over
# TLS, and Node's root store is compiled into the binary, so the patch level is the fix.
FROM node:22.23.4-alpine AS web-builder
WORKDIR /src/web
# Pin pnpm: `latest` started pulling v11, which made
# pnpm-workspace.yaml's onlyBuiltDependencies allow-list ineffective
# under --frozen-lockfile (v11 wants an interactive `pnpm approve-builds`
# step that has nowhere to run in a non-TTY Docker build), failing the
# image build with ERR_PNPM_IGNORED_BUILDS on msw/sharp/unrs-resolver.
RUN corepack enable && corepack prepare pnpm@10.15.0 --activate
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ .
RUN pnpm build

# --- Stage 2: Build Go binary ---
# Go 1.26.10 is part of the CVE-2026-111943 fix, not just hygiene: upstream added a
# crypto/x509 denylist for the compromised intermediates, so a binary built by an older
# toolchain keeps trusting them even when the system store is clean. go.mod already
# requires 1.26.10; this pins the image so the toolchain cannot resolve backwards.
FROM golang:1.26.10-alpine AS go-builder
RUN apk add --no-cache git
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Embed the built web UI
COPY --from=web-builder /src/web/out internal/setup/web
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown
# Stamp BOTH symbol sets — `main.*` for the legacy `fastagent version` CLI
# consumer and `internal/buildinfo.*` for the agent runtime + the About
# page in the web UI. Mirrors the Makefile / scripts/release.sh ldflags
# so a docker-built image identifies itself the same way the released
# binary does; without the buildinfo line the About page silently shows
# "dev" on every published image (the symptom that triggered this fix).
RUN CGO_ENABLED=0 go build \
    -ldflags "-s -w \
      -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE} \
      -X github.com/fastclaw-ai/fastclaw/internal/buildinfo.Version=${VERSION} \
      -X github.com/fastclaw-ai/fastclaw/internal/buildinfo.Commit=${COMMIT} \
      -X github.com/fastclaw-ai/fastclaw/internal/buildinfo.Date=${DATE}" \
    -o /fastagent ./cmd/fastclaw

# --- Stage 3: Runtime ---
# ca-certificates: the eMudhra roll (CVE-2026-111943, 2026-10-22). alpine v3.21/main carries
# ca-certificates-bundle 20261020-r0 with the two roots blacklisted, but a cached apt/apk index
# can still resolve the older 20260413-r0 — so the build asserts the CONTENT instead of trusting
# the version. openssl is a throwaway package: it is only needed to read the bundle.
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata \
 && apk add --no-cache --virtual .ca-check openssl \
 && ! openssl crl2pkcs7 -nocrl -certfile /etc/ssl/certs/ca-certificates.crt \
        | openssl pkcs7 -print_certs -noout \
        | grep -q -e 'CN=emSign ECC Root CA - G3$' -e 'CN=AAA Certificate Services$' \
 && apk del .ca-check
COPY --from=go-builder /fastagent /usr/local/bin/fastagent

# Default data directory. Override at runtime with FASTAGENT_HOME, but the
# default value here lets `docker run tokenaissance/fastagent` work with no env.
ENV FASTAGENT_HOME=/data/.fastagent \
    HOME=/data
RUN mkdir -p /data/.fastagent /data/.fastagent/skills
VOLUME /data/.fastagent

# Bundle built-in skills
COPY skills/ /data/.fastagent/skills/

EXPOSE 18953
ENTRYPOINT ["fastagent"]
CMD ["gateway"]
