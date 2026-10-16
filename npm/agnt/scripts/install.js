#!/usr/bin/env node

const crypto = require('crypto');
const https = require('https');
const http = require('http');
const fs = require('fs');
const path = require('path');
const { execFileSync } = require('child_process');
const { pipeline } = require('stream/promises');

// Read version from package.json to stay in sync
const packageJson = require('../package.json');
const VERSION = packageJson.version;
const REPO = 'standardbeagle/agnt';
const BINARY_NAME = 'agnt';
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
  // Use a different name for the actual binary to avoid conflict with the wrapper script
  return process.platform === 'win32' ? `${BINARY_NAME}-binary.exe` : `${BINARY_NAME}-binary`;
}

function getAssetName() {
  const platform = getPlatform();
  const ext = platform === 'windows' ? '.exe' : '';
  return `${BINARY_NAME}-${platform}-${getArch()}${ext}`;
}

// releaseAssetUrl is the GitHub release asset URL pattern.
function releaseAssetUrl(asset) {
  return `https://github.com/${REPO}/releases/download/v${VERSION}/${asset}`;
}

// expectedChecksum returns the hex SHA-256 that sums (a release's SHA256SUMS,
// sha256sum format: "<hex>  <name>" or "<hex> *<name>") lists for asset.
function expectedChecksum(sums, asset) {
  for (const line of sums.split(/\r?\n/)) {
    const fields = line.trim().split(/\s+/);
    if (fields.length !== 2 || fields[1].replace(/^\*/, '') !== asset) {
      continue;
    }
    const sum = fields[0].toLowerCase();
    if (!/^[0-9a-f]{64}$/.test(sum)) {
      throw new Error(`SHA256SUMS entry for ${asset} is not a SHA-256 digest: ${JSON.stringify(fields[0])}`);
    }
    return sum;
  }
  throw new Error(`SHA256SUMS lists no ${asset}`);
}

// verifyChecksum throws unless the file at filePath has the SHA-256 that sums
// lists for asset; a missing entry or a malformed one throws too.
function verifyChecksum(filePath, sums, asset) {
  const want = expectedChecksum(sums, asset);
  const got = crypto.createHash('sha256').update(fs.readFileSync(filePath)).digest('hex');
  if (got !== want) {
    throw new Error(`Checksum mismatch for ${asset}: downloaded file has sha256 ${got}, SHA256SUMS lists ${want}`);
  }
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
  return out === `${BINARY_NAME} v${VERSION}`;
}

function unlinkQuietly(p) {
  try {
    fs.unlinkSync(p);
    return true;
  } catch (e) {
    return false;
  }
}

async function install() {
  const binDir = path.join(__dirname, '..', 'bin');
  const binaryPath = path.join(binDir, getBinaryName());
  const oldPath = binaryPath + '.old';
  const newPath = binaryPath + '.new';

  // Create bin directory if it doesn't exist
  if (!fs.existsSync(binDir)) {
    fs.mkdirSync(binDir, { recursive: true });
  }

  // Skip only a binary that runs and reports this version; a truncated or
  // stale one from an earlier install is replaced.
  if (fs.existsSync(binaryPath)) {
    if (isExpectedVersion(reportedVersion(binaryPath))) {
      console.log(`${BINARY_NAME} v${VERSION} already installed, skipping download`);
      return;
    }
    console.log(`Existing ${BINARY_NAME} binary is not a working v${VERSION}; replacing it`);
  }

  // Download beside the final path and move it into place only once the
  // file is complete and runs, so an interrupted install never leaves a
  // truncated binary at binaryPath.
  const ext = process.platform === 'win32' ? '.exe' : '';
  const tmpPath = path.join(binDir, `.${BINARY_NAME}.${process.pid}.download${ext}`);

  const asset = getAssetName();
  const url = releaseAssetUrl(asset);
  const sumsPath = path.join(binDir, `.${BINARY_NAME}.${process.pid}.SHA256SUMS`);
  console.log(`Downloading ${BINARY_NAME} v${VERSION}...`);
  console.log(`  Platform: ${getPlatform()}`);
  console.log(`  Architecture: ${getArch()}`);
  console.log(`  URL: ${url}`);

  try {
    // The release's SHA256SUMS must vouch for the binary before it is run
    // or moved into place; without it the install fails.
    let sums;
    try {
      await downloadFile(releaseAssetUrl('SHA256SUMS'), sumsPath);
      sums = fs.readFileSync(sumsPath, 'utf8');
    } catch (e) {
      throw new Error(`Cannot verify the download, refusing to install: fetching SHA256SUMS for v${VERSION} failed: ${e.message}`);
    } finally {
      unlinkQuietly(sumsPath);
    }
    expectedChecksum(sums, asset); // a missing entry fails before the download
    await downloadFile(url, tmpPath);
    verifyChecksum(tmpPath, sums, asset);

    // Make executable on Unix
    if (process.platform !== 'win32') {
      fs.chmodSync(tmpPath, 0o755);
    }

    const out = reportedVersion(tmpPath);
    if (!isExpectedVersion(out)) {
      throw new Error(`Downloaded binary does not run as v${VERSION} (--version: ${out === null ? 'failed' : JSON.stringify(out)})`);
    }
  } catch (error) {
    unlinkQuietly(tmpPath);
    console.error(`Failed to install ${BINARY_NAME}:`);
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

  // The verified binary is complete. Move the existing one aside (a rename
  // works on Windows even while a running daemon holds it), then put the new
  // one in place. If the existing one cannot be moved, stage the new one as
  // .new, which the bin/agnt wrappers apply on their next run.
  if (fs.existsSync(binaryPath)) {
    unlinkQuietly(oldPath); // a leftover from a previous upgrade, may be locked
    try {
      fs.renameSync(binaryPath, oldPath);
    } catch (e) {
      unlinkQuietly(newPath);
      fs.renameSync(tmpPath, newPath);
      console.log(`Existing binary could not be replaced (${e.message}); staged v${VERSION} as ${path.basename(newPath)}, applied on the next 'agnt' run`);
      return;
    }
    if (!unlinkQuietly(oldPath)) {
      console.log(`Note: Old binary still in use (daemon running)`);
      console.log(`Tip: Run 'agnt upgrade' after install to restart daemon with new version`);
    }
  }

  fs.renameSync(tmpPath, binaryPath);
  // A .new staged by an earlier install would replace this one on the next run.
  unlinkQuietly(newPath);
  console.log(`Successfully installed ${BINARY_NAME} to ${binaryPath}`);
}

if (require.main === module) {
  install().catch((err) => {
    console.error(err);
    process.exit(1);
  });
}

module.exports = { expectedChecksum, verifyChecksum };
