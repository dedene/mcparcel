// Shared setup for the packaging tests. Not a test file: the Makefile runs
// tests/packaging/*.mjs only.
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { lstatSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

export const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..', '..');
export const packageDir = path.join(repoRoot, 'packaging', 'npm');
export const pkg = JSON.parse(readFileSync(path.join(packageDir, 'package.json'), 'utf8'));

// freshEnv is the whole environment npm and the binary get: a fresh HOME,
// XDG dirs, TMPDIR and npm cache under root, an empty npm user config (never
// ~/.npmrc), npm offline, SHELL=/bin/sh so the daemon's login shell reads no
// dotfiles, and a private runtime dir under /tmp. Without
// MCPARCEL_RUNTIME_DIR a runtime command would reach the real daemon.
// Nothing else comes from process.env but PATH.
export function freshEnv(root) {
  const dirs = { HOME: 'home', XDG_CONFIG_HOME: 'config', XDG_DATA_HOME: 'data', XDG_STATE_HOME: 'state', XDG_CACHE_HOME: 'cache', TMPDIR: 'tmp' };
  const env = { PATH: process.env.PATH, SHELL: '/bin/sh', LANG: 'C', LC_ALL: 'C' };
  for (const [name, dir] of Object.entries(dirs)) {
    env[name] = path.join(root, dir);
    mkdirSync(env[name], { recursive: true, mode: 0o700 });
  }
  // A short path: the socket must fit in 104 bytes.
  env.MCPARCEL_RUNTIME_DIR = mkdtempSync('/tmp/mcp-pkg-');
  const npmrc = path.join(root, 'npmrc');
  writeFileSync(npmrc, '');
  Object.assign(env, {
    npm_config_cache: path.join(root, 'npm cache'),
    npm_config_userconfig: npmrc,
    npm_config_offline: 'true',
    npm_config_audit: 'false',
    npm_config_fund: 'false',
    npm_config_update_notifier: 'false',
  });
  return env;
}

// removeFresh removes what freshEnv made outside root.
export function removeFresh(env) {
  if (env?.MCPARCEL_RUNTIME_DIR?.startsWith('/tmp/mcp-pkg-')) {
    rmSync(env.MCPARCEL_RUNTIME_DIR, { recursive: true, force: true });
  }
}

// packAndInstall packs the package into workDir and installs it under a
// prefix with a space in its name; it returns the tarball and the launcher.
export function packAndInstall(workDir, env) {
  execFileSync('npm', ['pack', '--pack-destination', workDir], { cwd: packageDir, env, stdio: 'pipe' });
  const tarball = readdirSync(workDir).find((name) => name.endsWith('.tgz'));
  assert.ok(tarball, 'npm pack produced no tarball');
  const prefix = path.join(workDir, 'install here');
  mkdirSync(prefix);
  execFileSync('npm', ['install', '--prefix', prefix, path.join(workDir, tarball)], { env, stdio: 'pipe' });
  return { tarball: path.join(workDir, tarball), launcher: path.join(prefix, 'node_modules', '.bin', 'mcparcel') };
}

// snapshot lists every entry below the given roots with type, mode, size and
// mtime, so a before/after comparison catches any write.
export function snapshot(roots) {
  const out = [];
  const walk = (p) => {
    const st = lstatSync(p);
    out.push(`${p} ${st.mode.toString(8)} ${st.size} ${st.mtimeMs}`);
    if (st.isDirectory()) {
      for (const name of readdirSync(p).sort()) walk(path.join(p, name));
    }
  };
  for (const root of roots) walk(root);
  return out;
}

export function makeWorkDir(name) {
  // The directory name contains a space on purpose.
  return mkdtempSync(path.join(tmpdir(), `mcparcel ${name} `));
}
