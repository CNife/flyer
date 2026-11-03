#!/usr/bin/env node
'use strict';

const { spawn } = require('node:child_process');
const { ensureBinary } = require('../lib/install');

async function main() {
  const binary = await ensureBinary();
  const child = spawn(binary, process.argv.slice(2), { stdio: 'inherit' });

  for (const signal of ['SIGINT', 'SIGTERM']) {
    process.on(signal, () => {
      try {
        child.kill(signal);
      } catch {
        // the child may already be gone
      }
    });
  }

  child.on('error', (err) => {
    console.error(`flyer: ${err.message}`);
    process.exit(1);
  });
  child.on('exit', (code, signal) => {
    if (signal) {
      try {
        process.kill(process.pid, signal);
        return;
      } catch {
        // fall through to the numeric exit code
      }
    }
    process.exit(code ?? 1);
  });
}

main().catch((err) => {
  console.error(`flyer: ${err.message}`);
  process.exit(1);
});
