// Measures what the npm layers add to one `mcparcel version` call.
// Usage: make npm-binary && node scripts/bench-launch.mjs [runs]
import { spawnSync } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';

import { freshEnv, packAndInstall, packageDir, removeFresh } from '../tests/packaging/lib/fresh.mjs';

const runs = Number(process.argv[2] ?? 20);
const workDir = mkdtempSync(path.join(tmpdir(), 'mcparcel-bench-'));
// A fresh HOME and npm config, as the packaging tests use: no ~/.npmrc.
const env = freshEnv(path.join(workDir, 'home'));
const { tarball, launcher } = packAndInstall(workDir, env);
const prefix = path.dirname(path.dirname(path.dirname(launcher)));

const variants = {
  'native binary': [path.join(packageDir, 'dist', 'mcparcel'), ['version']],
  'node launcher': [launcher, ['version']],
  'npx (installed)': ['npx', ['--prefix', prefix, 'mcparcel', 'version']],
  'npx (tarball, warm cache)': ['npx', ['--yes', '--package', tarball, 'mcparcel', 'version']],
};

function percentile(sorted, p) {
  return sorted[Math.min(sorted.length - 1, Math.floor(sorted.length * p))];
}

console.log(`runs per variant: ${runs}`);
console.log('variant                      p50 ms   p95 ms');
for (const [name, [command, args]] of Object.entries(variants)) {
  const samples = [];
  for (let i = 0; i <= runs; i += 1) {
    const start = process.hrtime.bigint();
    const result = spawnSync(command, args, { env, stdio: 'pipe' });
    const elapsed = Number(process.hrtime.bigint() - start) / 1e6;
    if (result.status !== 0) {
      throw new Error(`${name} failed: ${result.stderr}`);
    }
    if (i > 0) samples.push(elapsed); // the first run warms caches
  }
  samples.sort((a, b) => a - b);
  console.log(`${name.padEnd(28)} ${percentile(samples, 0.5).toFixed(0).padStart(6)}   ${percentile(samples, 0.95).toFixed(0).padStart(6)}`);
}
removeFresh(env);
rmSync(workDir, { recursive: true, force: true });
