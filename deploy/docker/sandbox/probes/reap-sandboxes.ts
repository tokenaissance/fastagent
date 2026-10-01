/**
 * Reap the sandboxes an image roll has to replace.
 *
 *   E2B_API_KEY=… bun probes/reap-sandboxes.ts --dry-run
 *   E2B_API_KEY=… bun probes/reap-sandboxes.ts [--template <templateID-or-alias>]
 *
 * Why this is part of a release and not a one-off: the pool keys sandboxes by
 * SCOPE and adopts whatever the lease names (internal/sandbox/e2b_executor.go,
 * adoptFromLease), so a rebuilt template is only picked up by scopes that do
 * not have a sandbox yet. Every existing scope keeps adopting its old sandbox —
 * and therefore the old image — until that sandbox is gone. Rolling the sandbox
 * image means killing the sandboxes that predate the roll; each scope then
 * rebuilds (create + hydrate) from the template on its next call.
 *
 * The 2026-10-01 roll is the worked example: dev and prod templates were
 * rebuilt from `…/fastagent-sandbox:20261001065937`, and the sandboxes still
 * running the old image were killed here. Same-day, dev also needed
 * `kubectl rollout restart deploy/fastagent-gateway -n development`, because
 * its pods had resolved `FASTAGENT_SANDBOX_IMAGE` from the configmap on 09-29
 * (pre-rename default `fastagent-sandbox`) and envFrom only re-resolves on
 * container start. Check the env before blaming the template:
 *
 *   kubectl -n <ns> exec <pod> -- printenv FASTAGENT_SANDBOX_IMAGE
 */
import { Sandbox } from 'e2b'

const args = process.argv.slice(2)
const dryRun = args.includes('--dry-run')
const wantTemplate = args.includes('--template') ? args[args.indexOf('--template') + 1] : undefined

const page = await Sandbox.list()
const items = await page.nextItems()

let killed = 0
for (const s of items) {
  const template = String((s as any).templateId ?? '?')
  if (wantTemplate && template !== wantTemplate) continue
  const started = s.startedAt ? new Date(s.startedAt).toISOString() : '?'
  if (dryRun) {
    console.log(`would kill ${s.sandboxId} template=${template} started=${started}`)
  } else {
    await Sandbox.kill(s.sandboxId)
    console.log(`killed ${s.sandboxId} template=${template} started=${started}`)
  }
  killed++
}
console.log(`${dryRun ? 'would kill' : 'killed'} ${killed} of ${items.length}`)
