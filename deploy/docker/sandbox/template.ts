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
  .setStartCmd('sudo sleep infinity', 'sleep 20')
