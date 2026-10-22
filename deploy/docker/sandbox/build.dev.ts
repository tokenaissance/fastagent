import { Template, defaultBuildLogger } from 'e2b'
import { template } from './template'

async function main() {
  await Template.build(template, 'fastagent-sandbox', {
    // CACHE-BUSTING IS NOT OPTIONAL (2026-10-22, CVE-2026-111943). E2B caches the layers it
    // bakes; a rebuild whose definition text did not change would hand out a template that
    // still carries the old CA bundle. skipCache is the only thing that guarantees the
    // sandbox image's stripped bundle is what gets baked.
    onBuildLogs: defaultBuildLogger(),
    skipCache: true,
  });
}

main().catch(console.error);
