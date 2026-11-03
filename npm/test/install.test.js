'use strict';

const assert = require('node:assert/strict');
const test = require('node:test');

const {
  assetName,
  parseChecksums,
  platformInfo,
} = require('../lib/install');

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
