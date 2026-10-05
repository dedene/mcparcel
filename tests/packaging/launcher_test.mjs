import assert from 'node:assert/strict';
import { execFileSync, spawn, spawnSync } from 'node:child_process';
import { copyFileSync, existsSync, mkdirSync, mkdtempSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { after, before, test } from 'node:test';
import { fileURLToPath } from 'node:url';

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');
const packageDir = path.join(repoRoot, 'packaging', 'npm');

let workDir;
let launcher;

before(() => {
  assert.ok(
    existsSync(path.join(packageDir, 'dist', 'mcparcel')),
    'run `make npm-binary` first: packaging/npm/dist/mcparcel is missing',
  );
  // The directory name contains a space on purpose.
  workDir = mkdtempSync(path.join(tmpdir(), 'mcparcel pack '));
  const cache = path.join(workDir, 'npm cache');
  const prefix = path.join(workDir, 'install here');
  mkdirSync(prefix);
  const env = { ...process.env, npm_config_cache: cache, npm_config_update_notifier: 'false' };

  execFileSync('npm', ['pack', '--pack-destination', workDir], { cwd: packageDir, env, stdio: 'pipe' });
  const tarball = readdirSync(workDir).find((name) => name.endsWith('.tgz'));
  assert.ok(tarball, 'npm pack produced no tarball');
  execFileSync('npm', ['install', '--prefix', prefix, path.join(workDir, tarball)], { env, stdio: 'pipe' });
  launcher = path.join(prefix, 'node_modules', '.bin', 'mcparcel');
});

after(() => {
  if (workDir) rmSync(workDir, { recursive: true, force: true });
});

test('installed launcher prints the version on stdout', () => {
  const result = spawnSync(launcher, ['version'], { encoding: 'utf8' });
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /^mcparcel \S+/);
  assert.equal(result.stderr, '');
});

test('installed launcher propagates a usage exit code', () => {
  const result = spawnSync(launcher, ['definitely-not-a-command'], { encoding: 'utf8' });
  assert.equal(result.status, 2);
  assert.equal(result.stdout, '');
});

test('arguments containing spaces arrive as one argument', () => {
  const result = spawnSync(launcher, ['two words'], { encoding: 'utf8' });
  assert.equal(result.status, 2);
  assert.match(result.stderr, /two words/);
});

test('SIGINT sent to the launcher reaches the native process', async () => {
  const child = spawn(launcher, ['spike', 'wait'], { stdio: ['ignore', 'pipe', 'pipe'] });
  await new Promise((resolve, reject) => {
    child.stdout.once('data', resolve);
    child.once('error', reject);
  });
  child.kill('SIGINT');
  const [code, signal] = await new Promise((resolve) => child.once('exit', (c, s) => resolve([c, s])));
  assert.equal(signal, null);
  assert.equal(code, 130);
});

test('a missing native executable gives one clear English error', () => {
  const brokenDir = path.join(workDir, 'broken package');
  mkdirSync(path.join(brokenDir, 'bin'), { recursive: true });
  copyFileSync(path.join(packageDir, 'bin', 'mcparcel.cjs'), path.join(brokenDir, 'bin', 'mcparcel.cjs'));
  const result = spawnSync(process.execPath, [path.join(brokenDir, 'bin', 'mcparcel.cjs'), 'version'], { encoding: 'utf8' });
  assert.equal(result.status, 1);
  assert.equal(result.stdout, '');
  assert.match(result.stderr, /^mcparcel: could not start the native executable: /);
});

test('a non-executable native file gives one clear English error', () => {
  const brokenDir = path.join(workDir, 'non-executable package');
  mkdirSync(path.join(brokenDir, 'bin'), { recursive: true });
  mkdirSync(path.join(brokenDir, 'dist'));
  copyFileSync(path.join(packageDir, 'bin', 'mcparcel.cjs'), path.join(brokenDir, 'bin', 'mcparcel.cjs'));
  writeFileSync(path.join(brokenDir, 'dist', 'mcparcel'), 'not an executable\n', { mode: 0o644 });
  const result = spawnSync(process.execPath, [path.join(brokenDir, 'bin', 'mcparcel.cjs'), 'version'], { encoding: 'utf8' });
  assert.equal(result.status, 1);
  assert.equal(result.stdout, '');
  assert.match(result.stderr, /^mcparcel: could not start the native executable: /);
});
