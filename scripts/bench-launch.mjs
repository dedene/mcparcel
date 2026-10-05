// Measures what the npm layers add to one `mcparcel version` call.
// Usage: make npm-binary && node scripts/bench-launch.mjs [runs]
import { execFileSync, spawnSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, readdirSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const runs = Number(process.argv[2] ?? 20);
const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const packageDir = path.join(repoRoot, 'packaging', 'npm');
const workDir = mkdtempSync(path.join(tmpdir(), 'mcparcel-bench-'));
const prefix = path.join(workDir, 'install');
mkdirSync(prefix);
const env = { ...process.env, npm_config_cache: path.join(workDir, 'cache'), npm_config_update_notifier: 'false' };

execFileSync('npm', ['pack', '--pack-destination', workDir], { cwd: packageDir, env, stdio: 'pipe' });
const tarball = path.join(workDir, readdirSync(workDir).find((name) => name.endsWith('.tgz')));
execFileSync('npm', ['install', '--prefix', prefix, tarball], { env, stdio: 'pipe' });

const variants = {
  'native binary': [path.join(packageDir, 'dist', 'mcparcel'), ['version']],
  'node launcher': [path.join(prefix, 'node_modules', '.bin', 'mcparcel'), ['version']],
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
rmSync(workDir, { recursive: true, force: true });
