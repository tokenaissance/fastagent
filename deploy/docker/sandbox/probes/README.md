# Sandbox template probes

Two scripts, and only these two: they answer the questions an operator actually asks before or
after a release.

- `probe-template.ts` — is the template's browser usable, and how long does a sandbox take to
  become useful?
- `bake-template.ts` — bake `fastagent-sandbox-dev` / `fastagent-sandbox-prod` (or any name) from
  the image tag in the environment.

Everything else written while the browser was being fixed (one-off `coldstart*.ts`,
`native-probe*.ts`, `final-probe.ts`, and per-alias bake wrappers) was moved to the Trash: they were
the same probe at different stages of the debugging, and keeping them invites running the wrong one.

## Running a probe

    export E2B_API_KEY=$(kubectl --context do-nyc2-tokenaissance-nyc2 -n production \
      get secret fastagent-secrets -o jsonpath='{.data.E2B_API_KEY}' | base64 -d)
    cd fastagent/deploy/docker/sandbox
    bun probes/probe-template.ts fastagent-sandbox-dev      # or a templateID

Pass the **template ID** when the result has to be trustworthy. E2B resolves aliases fuzzily, so a
probe that says `fastagent-sandbox-camo040` can land on `fastagent-sandbox` — which is how one
earlier measurement came back with numbers that did not belong to the template it named.

## What the numbers mean

- `CREATE=` — a sandbox exists and can run a command (T1). Non-browser work pays only this:
  measured 1.5–3 s.
- `ls … .cache/camoufox` — whether the 1.3 GB browser cache reached the RUNTIME user (`user`,
  `HOME=/home/user`). When it does not, every `open` fails with a config or daemon error. This was
  the root cause on 2026-09-30: the image kept the cache at `/root/.cache`, and no sandbox saw it.
- `ls … camoufox-cli-*.sock` — the daemon socket. Present a couple of seconds after creation means
  the background warm from the start command worked.
- first `camoufox-cli open` — the cold path: 11–15 s, exit=0 (measured 2026-09-30 after the
  rebuild; before it, `open` never returned 0 at all).
- second `open` — the warm path: 0.5–0.9 s, exit=0.

Known transient, seen once on dev: the first `open` can fail with
`Failed to connect to daemon after 5 attempts` and succeed on the next call — the background warm
and the first user request race. One retry covers it; the shim's daemon wait is where a permanent
fix belongs.

## Baking a template

    source ~/.docker/do-creds.sh                        # DO_REGISTRY_USER / DO_REGISTRY_PASSWORD
    export E2B_API_KEY=…
    export FASTAGENT_SANDBOX_TAG=$(date +%Y%m%d%H%M%S)   # the tag the image was pushed under
    bun probes/bake-template.ts fastagent-sandbox-dev
    bun probes/bake-template.ts fastagent-sandbox-prod

The image name is one name (`registry.digitalocean.com/tokenaissance/fastagent-sandbox`) with a
timestamp tag (to the second), and the environment lives in the template name. Both templates are
baked from the same image; `template.ts` reads `FASTAGENT_SANDBOX_TAG`, so an unset tag is a
deliberate `latest` rather than an accident.
