#!/usr/bin/env node

const https = require('https');
const http = require('http');
const fs = require('fs');
const path = require('path');
const { execFileSync } = require('child_process');
const { pipeline } = require('stream/promises');

// The version comes from package.json, which scripts/release.sh bumps along
// with every other version file; the release workflow publishes a
// devtool-mcp-<os>-<arch> asset (a copy of agnt) under that tag.
const VERSION = require('../package.json').version;
const REPO = 'standardbeagle/agnt';
const BINARY_NAME = 'devtool-mcp';
const MAX_REDIRECTS = 10;
const IDLE_TIMEOUT_MS = 60000;

// Platform/architecture mapping
const PLATFORMS = {
  darwin: 'darwin',
  linux: 'linux',
  win32: 'windows',
};

const ARCHS = {
  x64: 'amd64',
  arm64: 'arm64',
};

function getPlatform() {
  const platform = PLATFORMS[process.platform];
  if (!platform) {
    throw new Error(`Unsupported platform: ${process.platform}`);
  }
  return platform;
}

function getArch() {
  const arch = ARCHS[process.arch];
  if (!arch) {
    throw new Error(`Unsupported architecture: ${process.arch}`);
  }
  return arch;
}

function getBinaryName() {
  return process.platform === 'win32' ? `${BINARY_NAME}.exe` : BINARY_NAME;
}

function getDownloadUrl() {
  const platform = getPlatform();
  const arch = getArch();
  const ext = platform === 'windows' ? '.exe' : '';

  // GitHub release asset URL pattern
  return `https://github.com/${REPO}/releases/download/v${VERSION}/${BINARY_NAME}-${platform}-${arch}${ext}`;
}

// downloadFile writes url's body to dest and resolves only when the whole
// body arrived: a connection dropped mid-body rejects (pipeline's premature
// close), and a body shorter than Content-Length rejects.
function downloadFile(url, dest, redirects = 0) {
  return new Promise((resolve, reject) => {
    const protocol = url.startsWith('https:') ? https : http;

    const request = protocol.get(url, (response) => {
      const { statusCode } = response;

      // Handle redirects
      if ([301, 302, 303, 307, 308].includes(statusCode) && response.headers.location) {
        response.resume();
        if (redirects >= MAX_REDIRECTS) {
          reject(new Error('Failed to download: too many redirects'));
          return;
        }
        const next = new URL(response.headers.location, url).toString();
        downloadFile(next, dest, redirects + 1).then(resolve, reject);
        return;
      }

      if (statusCode !== 200) {
        response.resume();
        reject(new Error(`Failed to download: ${statusCode} ${response.statusMessage}`));
        return;
      }

      const expected = parseInt(response.headers['content-length'], 10);
      pipeline(response, fs.createWriteStream(dest))
        .then(() => {
          const size = fs.statSync(dest).size;
          if (size === 0) {
            throw new Error('Failed to download: empty response body');
          }
          if (Number.isFinite(expected) && size !== expected) {
            throw new Error(`Failed to download: got ${size} of ${expected} bytes`);
          }
          resolve();
        })
        .catch(reject);
    });

    request.on('error', reject);
    request.setTimeout(IDLE_TIMEOUT_MS, () => {
      request.destroy(new Error(`Failed to download: no data for ${IDLE_TIMEOUT_MS / 1000}s`));
    });
  });
}

// reportedVersion returns the first line of `<binary> --version` ("agnt
// vX.Y.Z"; later lines describe the running daemon, whose version is not
// this binary's), or null when it cannot run (a truncated or foreign file).
function reportedVersion(binaryPath) {
  try {
    const out = execFileSync(binaryPath, ['--version'], {
      encoding: 'utf8',
      timeout: 15000,
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    return out.split(/\r?\n/)[0].trim();
  } catch (e) {
    return null;
  }
}

function isExpectedVersion(out) {
  return out !== null && out.endsWith(` v${VERSION}`);
}

function isExpectedBinary(binaryPath) {
  return isExpectedVersion(reportedVersion(binaryPath));
}

async function install() {
  const binDir = path.join(__dirname, '..', 'bin');
  const binaryPath = path.join(binDir, getBinaryName());

  // Create bin directory if it doesn't exist
  if (!fs.existsSync(binDir)) {
    fs.mkdirSync(binDir, { recursive: true });
  }

  // Skip only a binary that runs and reports this version; a truncated or
  // stale one from an earlier install is replaced.
  if (fs.existsSync(binaryPath)) {
    if (isExpectedBinary(binaryPath)) {
      console.log(`${BINARY_NAME} v${VERSION} already installed, skipping download`);
      return;
    }
    console.log(`Existing ${BINARY_NAME} binary is not a working v${VERSION}; replacing it`);
  }

  // Download beside the final path and rename into place only once the file
  // is complete and runs, so an interrupted install never leaves a truncated
  // binary at binaryPath.
  const ext = process.platform === 'win32' ? '.exe' : '';
  const tmpPath = path.join(binDir, `.${BINARY_NAME}.${process.pid}.download${ext}`);

  const url = getDownloadUrl();
  console.log(`Downloading ${BINARY_NAME} v${VERSION}...`);
  console.log(`  Platform: ${getPlatform()}`);
  console.log(`  Architecture: ${getArch()}`);
  console.log(`  URL: ${url}`);

  try {
    await downloadFile(url, tmpPath);

    // Make executable on Unix
    if (process.platform !== 'win32') {
      fs.chmodSync(tmpPath, 0o755);
    }

    const out = reportedVersion(tmpPath);
    if (!isExpectedVersion(out)) {
      throw new Error(`Downloaded binary does not run as v${VERSION} (--version: ${out === null ? 'failed' : JSON.stringify(out)})`);
    }

    fs.renameSync(tmpPath, binaryPath);
    console.log(`Successfully installed ${BINARY_NAME} to ${binaryPath}`);
  } catch (error) {
    try {
      fs.unlinkSync(tmpPath);
    } catch (e) {
      // never created, or already gone
    }
    console.error(`Failed to download ${BINARY_NAME}:`);
    console.error(error.message);
    console.error('');
    console.error('You can manually download the binary from:');
    console.error(`  https://github.com/${REPO}/releases/tag/v${VERSION}`);
    console.error('');
    console.error('Or build from source:');
    console.error(`  git clone https://github.com/${REPO}.git`);
    console.error('  cd agnt');
    console.error('  make build');
    process.exit(1);
  }
}

install().catch((err) => {
  console.error(err);
  process.exit(1);
});
