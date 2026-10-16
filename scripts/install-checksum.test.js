// Run: node --test scripts/install-checksum.test.js
//
// The checksum verification both npm postinstall scripts run before moving a
// downloaded binary into place (npm/agnt/scripts/install.js and
// scripts/install.js). Requiring them does not run the install.

const test = require('node:test');
const assert = require('node:assert');
const crypto = require('crypto');
const fs = require('fs');
const os = require('os');
const path = require('path');

const installers = {
  'npm/agnt': require('../npm/agnt/scripts/install.js'),
  'devtool-mcp': require('./install.js'),
};

const body = Buffer.from('#!/bin/sh\necho agnt v1.2.3\n');
const digest = crypto.createHash('sha256').update(body).digest('hex');
const other = 'ab'.repeat(32);

function writeAsset(t) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'agnt-checksum-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const file = path.join(dir, 'asset');
  fs.writeFileSync(file, body);
  return file;
}

for (const [name, { expectedChecksum, verifyChecksum }] of Object.entries(installers)) {
  test(`${name}: a matching digest verifies`, (t) => {
    const sums = `${other}  agnt-linux-amd64\n${digest.toUpperCase()} *agnt-linux-arm64\r\n`;
    assert.strictEqual(expectedChecksum(sums, 'agnt-linux-arm64'), digest);
    verifyChecksum(writeAsset(t), sums, 'agnt-linux-arm64');
  });

  test(`${name}: a mismatch throws`, (t) => {
    assert.throws(() => verifyChecksum(writeAsset(t), `${other}  agnt-linux-arm64\n`, 'agnt-linux-arm64'), /Checksum mismatch/);
  });

  test(`${name}: a missing entry throws`, (t) => {
    // A name that is only a prefix of a listed asset does not match.
    const sums = `${digest}  agnt-linux-arm64.exe\n`;
    assert.throws(() => verifyChecksum(writeAsset(t), sums, 'agnt-linux-arm64'), /SHA256SUMS lists no agnt-linux-arm64/);
    assert.throws(() => verifyChecksum(writeAsset(t), '', 'agnt-linux-arm64'), /SHA256SUMS lists no/);
  });

  test(`${name}: a malformed digest throws`, () => {
    assert.throws(() => expectedChecksum('xyz  agnt-linux-arm64\n', 'agnt-linux-arm64'), /not a SHA-256 digest/);
  });
}
