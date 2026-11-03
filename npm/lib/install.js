'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const http = require('node:http');
const https = require('node:https');
const os = require('node:os');
const path = require('node:path');
const { URL } = require('node:url');

const pkg = require('../package.json');

const DEFAULT_RELEASE_BASE = 'https://github.com/CNife/flyer/releases/download';
const MAX_CHECKSUM_BYTES = 1024 * 1024;
const MAX_BINARY_BYTES = 128 * 1024 * 1024;
const MAX_REDIRECTS = 5;

const PLATFORMS = {
  'linux-x64': { os: 'linux', arch: 'amd64', ext: '' },
  'linux-arm64': { os: 'linux', arch: 'arm64', ext: '' },
  'darwin-x64': { os: 'darwin', arch: 'amd64', ext: '' },
  'darwin-arm64': { os: 'darwin', arch: 'arm64', ext: '' },
  'win32-x64': { os: 'windows', arch: 'amd64', ext: '.exe' },
  'win32-arm64': { os: 'windows', arch: 'arm64', ext: '.exe' },
};

function releaseBase() {
  return (process.env.FLYER_RELEASE_BASE || DEFAULT_RELEASE_BASE).replace(/\/+$/, '');
}

function platformInfo(platform = process.platform, arch = process.arch) {
  const info = PLATFORMS[`${platform}-${arch}`];
  if (!info) {
    throw new Error(
      `unsupported platform ${platform}/${arch}; install a binary from https://github.com/CNife/flyer/releases`,
    );
  }
  return info;
}

function assetName(version, goos, goarch) {
  const ext = goos === 'windows' ? '.exe' : '';
  return `flyer_${version}_${goos}_${goarch}${ext}`;
}

function checksumsUrl(version) {
  return `${releaseBase()}/v${version}/checksums.txt`;
}

function binaryUrl(version, asset) {
  return `${releaseBase()}/v${version}/${asset}`;
}

function parseChecksums(text) {
  const sums = new Map();
  for (const raw of text.split(/\r?\n/)) {
    const line = raw.trim();
    if (!line || line.startsWith('#')) continue;
    const match = /^([a-fA-F0-9]{64})\s+\*?(.+?)\s*$/.exec(line);
    if (match) sums.set(match[2], match[1].toLowerCase());
  }
  return sums;
}

function cacheRoot() {
  if (process.env.FLYER_CACHE_DIR) return process.env.FLYER_CACHE_DIR;
  if (process.platform === 'win32' && process.env.LOCALAPPDATA) {
    return path.join(process.env.LOCALAPPDATA, 'flyer', 'cache');
  }
  return path.join(process.env.XDG_CACHE_HOME || path.join(os.homedir(), '.cache'), 'flyer');
}

function binaryPath(version, info) {
  return path.join(cacheRoot(), version, `${info.os}-${info.arch}`, `flyer${info.ext}`);
}

function isUsable(file) {
  try {
    return fs.statSync(file).size > 0;
  } catch {
    return false;
  }
}

function request(url, redirects = 0) {
  if (redirects > MAX_REDIRECTS) {
    return Promise.reject(new Error(`too many redirects for ${url}`));
  }
  const parsed = new URL(url);
  const transport = parsed.protocol === 'https:' ? https : parsed.protocol === 'http:' ? http : null;
  if (!transport) {
    return Promise.reject(new Error(`unsupported URL ${url}`));
  }
  return new Promise((resolve, reject) => {
    const req = transport.get(
      parsed,
      { headers: { 'user-agent': `flyer/${pkg.version}` } },
      (res) => {
        const status = res.statusCode || 0;
        if (status >= 300 && status < 400 && res.headers.location) {
          res.resume();
          resolve(request(new URL(res.headers.location, parsed).toString(), redirects + 1));
          return;
        }
        if (status !== 200) {
          res.resume();
          reject(new Error(`download ${url} failed: HTTP ${status}`));
          return;
        }
        resolve(res);
      },
    );
    req.on('error', reject);
    req.setTimeout(30000, () => req.destroy(new Error(`download ${url} timed out`)));
  });
}

async function readText(url, maxBytes = MAX_CHECKSUM_BYTES) {
  const res = await request(url);
  const chunks = [];
  let size = 0;
  for await (const chunk of res) {
    size += chunk.length;
    if (size > maxBytes) {
      res.destroy();
      throw new Error(`download ${url} is too large`);
    }
    chunks.push(chunk);
  }
  return Buffer.concat(chunks).toString('utf8');
}

async function download(url, dest) {
  const res = await request(url);
  await new Promise((resolve, reject) => {
    const out = fs.createWriteStream(dest, { mode: 0o700 });
    let size = 0;
    res.on('data', (chunk) => {
      size += chunk.length;
      if (size > MAX_BINARY_BYTES) {
        res.destroy(new Error(`download ${url} is too large`));
      }
    });
    res.on('error', reject);
    out.on('error', reject);
    out.on('finish', resolve);
    res.pipe(out);
  });
}

function sha256(file) {
  const hash = crypto.createHash('sha256');
  hash.update(fs.readFileSync(file));
  return hash.digest('hex');
}

function equalHex(leftHex, rightHex) {
  const left = Buffer.from(leftHex, 'hex');
  const right = Buffer.from(rightHex, 'hex');
  return left.length > 0 && left.length === right.length && crypto.timingSafeEqual(left, right);
}

async function ensureBinary() {
  const info = platformInfo();
  const version = pkg.version;
  const dest = binaryPath(version, info);
  if (isUsable(dest)) return dest;

  const asset = assetName(version, info.os, info.arch);
  const sums = parseChecksums(await readText(checksumsUrl(version)));
  const want = sums.get(asset);
  if (!want) {
    throw new Error(`checksum for ${asset} not found in checksums.txt`);
  }

  const dir = path.dirname(dest);
  fs.mkdirSync(dir, { recursive: true, mode: 0o700 });
  const tmp = path.join(dir, `.${asset}.${process.pid}.${Date.now()}`);
  try {
    await download(binaryUrl(version, asset), tmp);
    const got = sha256(tmp);
    if (!equalHex(got, want)) {
      throw new Error(`checksum mismatch for ${asset}: got ${got}, want ${want}`);
    }
    if (process.platform !== 'win32') {
      fs.chmodSync(tmp, 0o755);
    }
    try {
      fs.renameSync(tmp, dest);
    } catch (err) {
      if (isUsable(dest)) {
        fs.unlinkSync(tmp);
        return dest;
      }
      throw err;
    }
  } catch (err) {
    try {
      fs.unlinkSync(tmp);
    } catch {
      // ignore cleanup errors
    }
    throw err;
  }
  return dest;
}

module.exports = {
  assetName,
  binaryPath,
  binaryUrl,
  cacheRoot,
  checksumsUrl,
  ensureBinary,
  parseChecksums,
  platformInfo,
};
