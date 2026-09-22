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
const REGISTRY_IMAGE =
  'registry.digitalocean.com/tokenaissance/fastagent-sandbox:latest'

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
