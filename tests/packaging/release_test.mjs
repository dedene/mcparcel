import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { existsSync, realpathSync, rmSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { after, before, test } from 'node:test';

import { freshEnv, makeWorkDir, packAndInstall, packageDir, removeFresh, snapshot } from './lib/fresh.mjs';

let workDir;
let tarball;
let launcher;
const envs = [];

before(() => {
  assert.ok(
    existsSync(path.join(packageDir, 'dist', 'mcparcel')),
    'run `make npm-binary` first: packaging/npm/dist/mcparcel is missing',
  );
  workDir = makeWorkDir('release');
  ({ tarball, launcher } = packAndInstall(workDir, fresh('pack home')));
});

after(() => {
  for (const env of envs) {
    // Always stop a runtime a test started, with the env that started it.
    if (env.started) {
      const stop = spawnSync(launcher, ['runtime', 'stop', '--force', '--json'], { env, encoding: 'utf8' });
      if (stop.status !== 0 && env.pid) {
        try {
          process.kill(env.pid, 'SIGTERM');
        } catch {
          // already gone
        }
      }
    }
    removeFresh(env);
  }
  if (workDir) rmSync(workDir, { recursive: true, force: true });
});

function fresh(name) {
  const env = freshEnv(path.join(workDir, name));
  envs.push(env);
  return env;
}

const json = (result) => {
  assert.equal(result.stderr, '', result.stderr);
  return JSON.parse(result.stdout);
};
const row = (data, id) => data.checks.find((c) => c.id === id);

test('doctor runs offline in a fresh HOME and writes nothing', () => {
  const env = fresh('doctor home');
  const roots = ['HOME', 'XDG_CONFIG_HOME', 'XDG_DATA_HOME', 'XDG_STATE_HOME', 'XDG_CACHE_HOME', 'TMPDIR', 'MCPARCEL_RUNTIME_DIR'].map((k) => env[k]);
  const before = snapshot(roots);
  const result = spawnSync(launcher, ['doctor', '--json'], { env, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
  assert.equal(result.status, 0, result.stdout + result.stderr);
  const out = json(result);
  assert.equal(out.ok, true);
  assert.equal(out.data.mode, 'desktop');
  assert.equal(row(out.data, 'runtime.version').status, 'ok');
  assert.match(row(out.data, 'runtime.binary').message, /^The next start copies this binary to /);
  assert.deepEqual(snapshot(roots), before);
});

test('a missing prerequisite is reported, not prompted', () => {
  const env = fresh('prereq home');
  // An absolute command with a space in its path, never created.
  const command = path.join(workDir, 'no such dir', 'mcparcel-missing-prereq');
  const definition = path.join(workDir, 'missing prereq.json');
  writeFileSync(definition, JSON.stringify({ id: 'missing', transport: { type: 'stdio', command } }));
  const opts = { env, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] };
  assert.equal(spawnSync(launcher, ['local', 'add', '--file', definition, '--json'], opts).status, 0);
  assert.equal(spawnSync(launcher, ['enable', 'local:missing', '--json'], opts).status, 0);
  const result = spawnSync(launcher, ['doctor', '--json', '--no-input'], opts);
  assert.equal(result.status, 8, result.stdout + result.stderr);
  const out = JSON.parse(result.stdout);
  assert.equal(out.ok, false);
  assert.equal(out.error.code, 'doctor_failed');
  const check = out.data.checks.find((c) => c.id === 'prereq.command' && c.subject === 'local:missing');
  assert.equal(check.status, 'fail');
  assert.equal(check.code, 'connection_failed');
  assert.ok(check.message.includes('mcparcel-missing-prereq'), check.message);
  // Nothing was started to find out.
  assert.equal(existsSync(path.join(env.MCPARCEL_RUNTIME_DIR, 'daemon.sock')), false);
});

test('npx from a cleaned npm cache keeps the runtime usable', () => {
  const env = fresh('npx home');
  const npx = (...args) => spawnSync('npm', ['exec', '--yes', '--package', tarball, '--', 'mcparcel', ...args], { env, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });

  const restarted = npx('runtime', 'restart', '--json');
  env.started = true;
  assert.equal(restarted.status, 0, restarted.stdout + restarted.stderr);
  const status = JSON.parse(restarted.stdout).data.status;
  env.pid = status.pid;
  // The daemon runs from its retained copy, not from the npx cache.
  const retained = path.join(realpathSync(env.XDG_DATA_HOME), 'mcparcel', 'runtime', status.binaryVersion, 'mcparcel');
  assert.equal(status.executable, retained);
  assert.ok(existsSync(retained));
  assert.ok(existsSync(path.join(env.npm_config_cache, '_npx')));

  rmSync(path.join(env.npm_config_cache, '_npx'), { recursive: true, force: true });

  const after = npx('runtime', 'status', '--json');
  assert.equal(after.status, 0, after.stdout + after.stderr);
  const data = JSON.parse(after.stdout).data;
  assert.equal(data.running, true);
  assert.equal(data.compatible, true);
  assert.equal(data.pid, status.pid);
  assert.equal(data.executable, retained);

  const doctor = npx('doctor', '--json');
  assert.equal(doctor.status, 0, doctor.stdout + doctor.stderr);
  const binary = row(JSON.parse(doctor.stdout).data, 'runtime.binary');
  assert.equal(binary.status, 'ok', binary.message);
});
