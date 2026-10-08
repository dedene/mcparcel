import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { copyFileSync, existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { after, before, test } from 'node:test';

import { freshEnv, makeWorkDir, packAndInstall, packageDir, pkg, removeFresh } from './lib/fresh.mjs';

let workDir;
let env;
let launcher;

before(() => {
  assert.ok(
    existsSync(path.join(packageDir, 'dist', 'mcparcel')),
    'run `make npm-binary` first: packaging/npm/dist/mcparcel is missing',
  );
  workDir = makeWorkDir('pack');
  env = freshEnv(path.join(workDir, 'fresh home'));
  ({ launcher } = packAndInstall(workDir, env));
});

after(() => {
  removeFresh(env);
  if (workDir) rmSync(workDir, { recursive: true, force: true });
});

const run = (args, options = {}) => spawnSync(launcher, args, { encoding: 'utf8', env, ...options });

test('installed launcher prints the version on stdout', () => {
  const result = run(['version']);
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /^mcparcel \S+/);
  assert.equal(result.stderr, '');
});

test('installed launcher reports the package.json version', () => {
  const result = run(['version']);
  assert.equal(result.status, 0, result.stderr);
  // <package.json version>+<commit>: the Makefile injects both.
  assert.ok(result.stdout.startsWith(`mcparcel ${pkg.version}+`), result.stdout);
  // A build from a tree with uncommitted changes says so: .dirty.<UTC seconds>.
  assert.match(result.stdout, /^mcparcel \S+\+([0-9a-f]{12})(\.dirty\.\d{14})? \(\1, /);
});

test('installed launcher propagates a usage exit code', () => {
  const result = run(['definitely-not-a-command']);
  assert.equal(result.status, 2);
  assert.equal(result.stdout, '');
});

test('arguments containing spaces arrive as one argument', () => {
  const result = run(['two words']);
  assert.equal(result.status, 2);
  assert.match(result.stderr, /two words/);
});

async function signalWait(signal) {
  const child = spawn(launcher, ['signal-wait'], { env, stdio: ['ignore', 'pipe', 'pipe'] });
  await new Promise((resolve, reject) => {
    child.stdout.once('data', resolve);
    child.once('error', reject);
  });
  child.kill(signal);
  return new Promise((resolve) => child.once('exit', (code, sig) => resolve([code, sig])));
}

test('SIGINT sent to the launcher reaches the native process', async () => {
  assert.deepEqual(await signalWait('SIGINT'), [130, null]);
});

test('SIGTERM sent to the launcher reaches the native process', async () => {
  // The native process catches SIGTERM like SIGINT and exits 130.
  assert.deepEqual(await signalWait('SIGTERM'), [130, null]);
});

test('SIGHUP ends the launcher with the same signal', async () => {
  // The native process does not catch SIGHUP and dies of it; the launcher
  // re-raises it on itself.
  assert.deepEqual(await signalWait('SIGHUP'), [null, 'SIGHUP']);
});

test('a missing native executable gives one clear English error', () => {
  const brokenDir = path.join(workDir, 'broken package');
  mkdirSync(path.join(brokenDir, 'bin'), { recursive: true });
  copyFileSync(path.join(packageDir, 'bin', 'mcparcel.cjs'), path.join(brokenDir, 'bin', 'mcparcel.cjs'));
  const result = spawnSync(process.execPath, [path.join(brokenDir, 'bin', 'mcparcel.cjs'), 'version'], { encoding: 'utf8', env });
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
  const result = spawnSync(process.execPath, [path.join(brokenDir, 'bin', 'mcparcel.cjs'), 'version'], { encoding: 'utf8', env });
  assert.equal(result.status, 1);
  assert.equal(result.stdout, '');
  assert.match(result.stderr, /^mcparcel: could not start the native executable: /);
});

test('the packaged binary is ad-hoc signed with a stable identifier', { skip: process.platform !== 'darwin' }, () => {
  // The installed copy, so npm pack and install kept the signature.
  const binary = path.join(path.dirname(launcher), '..', 'mcparcel', 'dist', 'mcparcel');
  const info = spawnSync('codesign', ['-dv', binary], { encoding: 'utf8' });
  assert.equal(info.status, 0, info.stderr);
  assert.match(info.stderr, /^Identifier=mcparcel$/m);
  assert.match(info.stderr, /^Signature=adhoc$/m);
  const verify = spawnSync('codesign', ['--verify', '--strict', binary], { encoding: 'utf8' });
  assert.equal(verify.status, 0, verify.stderr);
});

// tests/cli checks that the tagged test binary does contain these markers.
test('the release binary has no fixture stand-ins', () => {
  const binary = readFileSync(path.join(packageDir, 'dist', 'mcparcel'));
  for (const marker of ['fixture-terminal', 'fixture-dialog-answer', 'fixture-keyring', 'fixture-retain', 'fixture-apps', 'fixture-no-desktop-app', 'fixture-no-session-bus', 'fixture-no-desktop-session']) {
    assert.equal(binary.includes(marker), false, marker);
  }
});
