# Sandbox template probes

Three scripts, and only these three: they answer the questions an operator actually asks before,
during and after a release.

- `probe-template.ts` — is the template's browser usable, and how long does a sandbox take to
  become useful?
- `bake-template.ts` — bake `fastagent-sandbox-dev` / `fastagent-sandbox-prod` (or any name) from
  the image tag in the environment.
- `reap-sandboxes.ts` — kill the sandboxes that predate an image roll, because otherwise every
  existing scope keeps adopting its old sandbox (and its old image) from the lease.

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
- `ls … camoufox-cli-*.sock` — the daemon socket. A fresh sandbox should show **none**: the socket
  appears when the first browser call spawns its daemon. Until 2026-10-01 the start command warmed a
  daemon here, and that warm is what broke the first call — the platform kills everything the start
  command left behind about ten seconds after the sandbox is handed over, socket and browser with it,
  so a call attached at that moment died with `Failed to connect to daemon after 5 attempts:
  [Errno 2] No such file or directory`. A socket at creation time means someone put the warm back.
- first `camoufox-cli open` — the cold path: 5.4 s, exit=0 (measured 2026-10-01 with no warm at all;
  the 11–15 s measured on 2026-09-30 was a call queued behind the warm's own browser launch).
- second `open` — the warm path: 0.5–0.9 s, exit=0.

If a first `open` ever comes back as `Failed to connect to daemon after 5 attempts: [Errno 2|111]`,
the daemon it was attached to died under it. The image's shim
(`deploy/docker/sandbox/camoufox-cli-shim.sh`) re-runs that call once — such a call provably never
reached a daemon, so replaying it cannot replay a click — and says so on stderr. Seeing the retry
line means a daemon died; it is not the expected steady state.

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

## Rolling an image (the step that is easy to forget)

A rebaked template is only picked up by scopes that do **not** have a sandbox yet: the pool keys
sandboxes by scope and adopts whatever the lease names, so an existing scope keeps its old sandbox,
and the old image, until that sandbox is gone. So an image roll is three steps, not one:

1. check what the pods actually resolved (envFrom only re-resolves on container start):

       kubectl -n <ns> exec <gateway-pod> -- printenv FASTAGENT_SANDBOX_IMAGE

   If it names an older template than the configmap does, `kubectl rollout restart
   deploy/fastagent-gateway -n <ns>` — this is exactly what dev needed on 2026-10-01, where the pods
   had resolved the pre-rename default on 09-29.
2. `bun probes/bake-template.ts fastagent-sandbox-dev` (and `-prod`), from the pushed image tag.
3. `bun probes/reap-sandboxes.ts --dry-run`, then without `--dry-run`, so the next call in each
   scope rebuilds from the current template.
