#!/usr/bin/env node
'use strict';

const { spawn } = require('node:child_process');
const path = require('node:path');

const binary = path.join(__dirname, '..', 'dist', 'mcparcel');
const child = spawn(binary, process.argv.slice(2), { stdio: 'inherit' });

// A terminal Ctrl-C reaches the child directly through the process group.
// Forwarding covers signals sent to this launcher process only (agents, kill).
const forwarded = ['SIGINT', 'SIGTERM', 'SIGHUP'];
for (const signal of forwarded) {
  process.on(signal, () => {
    if (child.exitCode === null && child.signalCode === null) {
      child.kill(signal);
    }
  });
}

child.on('error', (error) => {
  console.error(`mcparcel: could not start the native executable: ${error.message}`);
  process.exit(1);
});

child.on('exit', (code, signal) => {
  if (signal) {
    for (const name of forwarded) {
      process.removeAllListeners(name);
    }
    process.kill(process.pid, signal);
    return;
  }
  process.exit(code ?? 1);
});
