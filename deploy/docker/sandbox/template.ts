import { Template } from 'e2b'

// The sandbox image is ours and it lives in our own registry, which is
// private — E2B pulls it while baking, so it needs credentials. They
// come from the environment and are never committed:
//
//   DO_REGISTRY_USER=… DO_REGISTRY_PASSWORD=… \
//     bun deploy/docker/sandbox/build.dev.ts
//
// `doctl registry login` writes the same pair into the local docker
// config, so `docker push` and this bake use one credential.
// The tag is NOT hardcoded to `latest` any more: the image and this template used to be mutable
// twice over (build.sh defaulted to `latest`, and this line pinned `latest`), so any push silently
// changed what every NEW sandbox got, dev and prod together, with no way back and no way to say
// which build a session had. Set the tag explicitly when baking:
//
//   DO_REGISTRY_USER=… DO_REGISTRY_PASSWORD=… FASTAGENT_SANDBOX_TAG=20260930-camoufox040 \
//     bun deploy/docker/sandbox/build.dev.ts
const SANDBOX_TAG = process.env.FASTAGENT_SANDBOX_TAG ?? 'latest'
const REGISTRY_IMAGE =
  `registry.digitalocean.com/tokenaissance/fastagent-sandbox:${SANDBOX_TAG}`

const registryUsername = process.env.DO_REGISTRY_USER
const registryPassword = process.env.DO_REGISTRY_PASSWORD

export const template = (
  registryUsername && registryPassword
    ? Template().fromImage(REGISTRY_IMAGE, {
        username: registryUsername,
        password: registryPassword,
      })
    : Template().fromImage(REGISTRY_IMAGE)
)
  .setUser('root')
  .setWorkdir('/')
  .setWorkdir('/workspace')
  .setUser('user')
  // NO warm-up here (removed 2026-10-01). It used to fire `camoufox-cli open about:blank` in the
  // background, and that warm is what broke the first browser call in a fresh sandbox: measured in
  // E2B, the platform kills everything the start command left behind about ten seconds after the
  // sandbox is handed over — daemon, socket and browser together (three runs, three kills; the
  // daemon had already been reparented to pid 1 and still died). A call attached at that moment
  // came back as `Failed to connect to daemon after 5 attempts: [Errno 2] No such file or
  // directory`, which is exactly what a user saw. A daemon started by the caller's own exec line is
  // untouched (survived 40 s+ across two opens, both exit 0), and with no warm at all the first
  // browser call in a fresh sandbox measured 5.4 s, exit 0 — faster than the warm ever made it,
  // because the warm's own browser launch was in the way. The shim's one retry
  // (deploy/docker/sandbox/camoufox-cli-shim.sh) stays as insurance for a daemon that dies under a
  // caller for any other reason.
  //
  // What is warmed instead is the Python import chain — no daemon, no socket, no browser, nothing
  // left behind. Measured in a fresh sandbox (2026-10-01): importing the daemon's chain costs 3.25 s
  // cold against 0.62 s warm, and the client waits only 5 s for its daemon's socket, so a cold
  // sandbox sat one slow boot away from `Daemon did not start within 5 seconds` (and a 56 s first
  // call, on a run where that happened). Reading the modules in the background costs ~3 s of one CPU
  // at boot and keeps the client inside its own budget.
  .setStartCmd(
    'sh -lc "nohup python3 -c \'import camoufox_cli.server\' >/dev/null 2>&1 & exec sleep infinity"',
    'sleep 5'
  )
