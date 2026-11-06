'use strict';

const assert = require('node:assert/strict');
const { execFile } = require('node:child_process');
const crypto = require('node:crypto');
const fs = require('node:fs');
const http = require('node:http');
const os = require('node:os');
const path = require('node:path');
const { promisify } = require('node:util');
const test = require('node:test');

const pkg = require('../package.json');
const {
  assetName,
  binaryPath,
  ensureBinary,
  parseChecksums,
  platformInfo,
} = require('../lib/install');

const execFileAsync = promisify(execFile);

test('assetName matches GoReleaser output', () => {
  assert.equal(assetName('0.1.0', 'linux', 'amd64'), 'flyer_0.1.0_linux_amd64');
  assert.equal(assetName('0.1.0', 'windows', 'arm64'), 'flyer_0.1.0_windows_arm64.exe');
});

test('platformInfo maps Node names to GoReleaser names', () => {
  assert.deepEqual(platformInfo('linux', 'x64'), { os: 'linux', arch: 'amd64', ext: '' });
  assert.deepEqual(platformInfo('darwin', 'arm64'), { os: 'darwin', arch: 'arm64', ext: '' });
  assert.deepEqual(platformInfo('win32', 'arm64'), { os: 'windows', arch: 'arm64', ext: '.exe' });
  assert.throws(() => platformInfo('freebsd', 'x64'), /unsupported platform/);
});

test('parseChecksums handles binary-mode markers', () => {
  const hash = 'a'.repeat(64);
  const sums = parseChecksums(
    `${hash}  flyer_0.1.0_linux_amd64\n${hash} *flyer_0.1.0_windows_amd64\n`,
  );
  assert.equal(sums.get('flyer_0.1.0_linux_amd64'), hash);
  assert.equal(sums.get('flyer_0.1.0_windows_amd64'), hash);
});

test('ensureBinary downloads, verifies, and reuses the cache', async (t) => {
  const binary = Buffer.from('#!/bin/sh\nprintf "fake\\n"\n');
  const base = await fixtureRelease(t, {
    body: binary,
    checksum: sha256(binary),
  });
  const cache = tempDir(t);
  useEnv(t, {
    FLYER_RELEASE_BASE: base.url,
    FLYER_CACHE_DIR: cache,
  });

  const dest = await ensureBinary();
  assert.equal(fs.readFileSync(dest).toString(), binary.toString());
  if (process.platform !== 'win32') {
    assert.equal(fs.statSync(dest).mode & 0o777, 0o755);
  }

  await base.close();
  assert.equal(await ensureBinary(), dest, 'second call should use the cache');
});

test('ensureBinary rejects a checksum mismatch and leaves no binary', async (t) => {
  const binary = Buffer.from('tampered binary');
  const base = await fixtureRelease(t, {
    body: binary,
    checksum: '0'.repeat(64),
  });
  const cache = tempDir(t);
  useEnv(t, {
    FLYER_RELEASE_BASE: base.url,
    FLYER_CACHE_DIR: cache,
  });

  await assert.rejects(ensureBinary(), /checksum mismatch/);
  const dest = binaryPath(pkg.version, platformInfo());
  assert.equal(fs.existsSync(dest), false);
});

test('ensureBinary rejects a missing checksum entry', async (t) => {
  const body = Buffer.from('binary without a checksum');
  const base = await fixtureRelease(t, {
    body,
    checksum: '1'.repeat(64),
    checksumAsset: 'flyer_0.0.0_other_amd64',
  });
  useEnv(t, {
    FLYER_RELEASE_BASE: base.url,
    FLYER_CACHE_DIR: tempDir(t),
  });

  await assert.rejects(ensureBinary(), /checksum for .* not found/);
});

test('bin/flyer.js spawns the downloaded binary', { skip: process.platform === 'win32' }, async (t) => {
  const binary = Buffer.from('#!/bin/sh\nprintf "fake-flyer:%s\\n" "$1"\n');
  const base = await fixtureRelease(t, {
    body: binary,
    checksum: sha256(binary),
  });
  const cache = tempDir(t);

  const { stdout } = await execFileAsync(
    process.execPath,
    [path.join(__dirname, '..', 'bin', 'flyer.js'), 'version'],
    {
      env: {
        ...process.env,
        FLYER_RELEASE_BASE: base.url,
        FLYER_CACHE_DIR: cache,
      },
      timeout: 10000,
    },
  );
  assert.equal(stdout.trim(), 'fake-flyer:version');
});

async function fixtureRelease(t, { body, checksum, checksumAsset }) {
  const info = platformInfo();
  const asset = checksumAsset || assetName(pkg.version, info.os, info.arch);
  const server = http.createServer((req, res) => {
    if (req.url === `/v${pkg.version}/checksums.txt`) {
      res.writeHead(200, { 'content-type': 'text/plain' });
      res.end(`${checksum}  ${asset}\n`);
      return;
    }
    if (req.url === `/v${pkg.version}/${assetName(pkg.version, info.os, info.arch)}`) {
      res.writeHead(200, { 'content-type': 'application/octet-stream' });
      res.end(body);
      return;
    }
    res.writeHead(404);
    res.end('not found');
  });
  await new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', resolve);
  });
  let closed = false;
  const close = () =>
    new Promise((resolve) => {
      if (closed) {
        resolve();
        return;
      }
      closed = true;
      server.close(resolve);
    });
  t.after(close);
  return {
    url: `http://127.0.0.1:${server.address().port}`,
    close,
  };
}

function tempDir(t) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'flyer-npm-test-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  return dir;
}

function useEnv(t, updates) {
  const previous = {};
  for (const [key, value] of Object.entries(updates)) {
    previous[key] = process.env[key];
    process.env[key] = value;
  }
  t.after(() => {
    for (const [key, value] of Object.entries(previous)) {
      if (value === undefined) {
        delete process.env[key];
      } else {
        process.env[key] = value;
      }
    }
  });
}

function sha256(buf) {
  return crypto.createHash('sha256').update(buf).digest('hex');
}
