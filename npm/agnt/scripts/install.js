#!/usr/bin/env node

const https = require('https');
const http = require('http');
const fs = require('fs');
const path = require('path');

// Read version from package.json to stay in sync
const packageJson = require('../package.json');
const VERSION = packageJson.version;
const REPO = 'standardbeagle/agnt';
const BINARY_NAME = 'agnt';

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

function getDownloadUrl() {
  const platform = getPlatform();
  const arch = getArch();
  const ext = platform === 'windows' ? '.exe' : '';

  // GitHub release asset URL pattern
  return `https://github.com/${REPO}/releases/download/v${VERSION}/${BINARY_NAME}-${platform}-${arch}${ext}`;
}

async function downloadFile(url, dest) {
  return new Promise((resolve, reject) => {
    const file = fs.createWriteStream(dest);
    const protocol = url.startsWith('https') ? https : http;

    const request = protocol.get(url, (response) => {
      // Handle redirects
      if (response.statusCode === 301 || response.statusCode === 302) {
        file.close();
        fs.unlinkSync(dest);
        downloadFile(response.headers.location, dest).then(resolve).catch(reject);
        return;
      }

      if (response.statusCode !== 200) {
        file.close();
        fs.unlinkSync(dest);
        reject(new Error(`Failed to download: ${response.statusCode} ${response.statusMessage}`));
        return;
      }

      response.pipe(file);
      file.on('finish', () => {
        file.close();
        resolve();
      });
    });

    request.on('error', (err) => {
      file.close();
      fs.unlinkSync(dest);
      reject(err);
    });
  });
}

async function install() {
  const binDir = path.join(__dirname, '..', 'bin');
  const binaryPath = path.join(binDir, getBinaryName());

  // Create bin directory if it doesn't exist
  if (!fs.existsSync(binDir)) {
    fs.mkdirSync(binDir, { recursive: true });
  }

  // Handle existing binary (might be locked on Windows if daemon is running)
  if (fs.existsSync(binaryPath)) {
    const oldPath = binaryPath + '.old';
    let binaryLocked = false;

    try {
      // Try to delete any previous .old file first
      if (fs.existsSync(oldPath)) {
        try {
          fs.unlinkSync(oldPath);
        } catch (e) {
          // Ignore - might still be locked from previous upgrade
        }
      }

      // Rename current binary to .old (works even if file is locked on Windows)
      fs.renameSync(binaryPath, oldPath);
      console.log(`Renamed existing binary to ${path.basename(oldPath)}`);

      // Try to delete the old file (will fail if locked, but that's ok)
      try {
        fs.unlinkSync(oldPath);
      } catch (e) {
        // File is locked (daemon running) - will be cleaned up next time
        binaryLocked = true;
        console.log(`Note: Old binary still in use (daemon running)`);
      }
    } catch (e) {
      // Rename failed - binary might be the same version or something else is wrong
      console.log(`${BINARY_NAME} binary already exists, skipping download`);
      return;
    }

    // If binary was locked, suggest running agnt upgrade after install
    if (binaryLocked) {
      console.log(`Tip: Run 'agnt upgrade' after install to restart daemon with new version`);
    }
  }

  const url = getDownloadUrl();
  console.log(`Downloading ${BINARY_NAME} v${VERSION}...`);
  console.log(`  Platform: ${getPlatform()}`);
  console.log(`  Architecture: ${getArch()}`);
  console.log(`  URL: ${url}`);

  try {
    await downloadFile(url, binaryPath);

    // Make executable on Unix
    if (process.platform !== 'win32') {
      fs.chmodSync(binaryPath, 0o755);
    }

    console.log(`Successfully installed ${BINARY_NAME} to ${binaryPath}`);
  } catch (error) {
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
