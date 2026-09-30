/**
 * Is a sandbox template's browser usable, and how long does a sandbox take to become useful?
 *
 *   E2B_API_KEY=… bun deploy/docker/sandbox/probes/probe-template.ts <templateID|alias>
 *
 * Every command is reported with its own exit code and stderr: a probe that swallows either is how
 * two earlier rounds concluded "the counter never fired" and "the template works" from the wrong
 * evidence. See probes/README.md for what the numbers mean.
 */
import { Sandbox } from 'e2b'

if (!process.env.E2B_API_KEY) {
  console.error('E2B_API_KEY is required (see probes/README.md)')
  process.exit(2)
}
const target = process.argv[2]
if (!target) {
  console.error('usage: bun probe-template.ts <templateID|alias>')
  process.exit(2)
}

const created = Date.now()
const sandbox = await Sandbox.create(target, { timeoutMs: 180_000 })
console.log(`CREATE=${Date.now() - created}ms  (${target})`)

const steps = [
  'whoami; echo HOME=$HOME',
  'ls -d /home/user/.cache/camoufox 2>/dev/null; ls /home/user/.cache/camoufox 2>/dev/null | head -3; echo (cache)',
  'ls -la /tmp/camoufox-cli-*.sock 2>&1 | head -2',
  'camoufox-cli open about:blank',
  'camoufox-cli open about:blank',
]

for (const cmd of steps) {
  const started = Date.now()
  let exit = 0
  let out = ''
  let err = ''
  try {
    const r = await sandbox.commands.run(cmd)
    exit = r.exitCode
    out = String(r.stdout ?? '')
    err = String(r.stderr ?? '')
  } catch (e: any) {
    exit = e.exitCode ?? -1
    out = String(e.stdout ?? '')
    err = String(e.stderr ?? e.message ?? e)
  }
  console.log(`$ ${cmd}\n  ${Date.now() - started}ms exit=${exit}`)
  if (out.trim()) console.log(`  OUT ${out.trim().slice(0, 400)}`)
  if (err.trim()) console.log(`  ERR ${err.trim().slice(0, 400)}`)
}

await sandbox.kill()
