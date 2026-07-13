import { Template, defaultBuildLogger } from 'e2b'
import { template } from './template'

async function main() {
  await Template.build(template, 'fastclaw-sandbox-dev', {
    onBuildLogs: defaultBuildLogger(),
  });
}

main().catch(console.error);