/**
 * Bake one E2B template from the sandbox image tag in the environment.
 *
 *   FASTAGENT_SANDBOX_TAG=20260930220914 DO_REGISTRY_USER=… DO_REGISTRY_PASSWORD=… E2B_API_KEY=… \
 *     bun deploy/docker/sandbox/probes/bake-template.ts fastagent-sandbox-dev
 *
 * Replaces the per-alias wrappers written during the browser fix, which differed only in the name
 * they passed. The image name is fixed (`…/fastagent-sandbox`); the tag comes from the environment
 * so a bake cannot pick up a different build by accident; the environment lives in the template
 * name.
 */
import { Template, defaultBuildLogger } from 'e2b'
import { template } from '../template'

const name = process.argv[2]
const tag = process.env.FASTAGENT_SANDBOX_TAG

if (!name || !tag) {
  console.error(
    'usage: FASTAGENT_SANDBOX_TAG=<tag> DO_REGISTRY_USER=… DO_REGISTRY_PASSWORD=… E2B_API_KEY=… ' +
      'bun bake-template.ts <template-name>'
  )
  process.exit(2)
}

console.log(`baking ${name} from registry.digitalocean.com/tokenaissance/fastagent-sandbox:${tag}`)
await Template.build(template, name, { onBuildLogs: defaultBuildLogger() })
