#!/usr/bin/env node

/**
 * Pre-pack script for npm publishing.
 *
 * This script runs before `npm pack` to ensure the package is ready for publishing.
 * It does NOT include any binaries - those are downloaded at install time.
 */

const fs = require('fs');
const path = require('path');

const rootDir = path.join(__dirname, '..');
const binDir = path.join(rootDir, 'bin');
const distDir = path.join(rootDir, 'dist');

// Ensure directories exist
if (!fs.existsSync(binDir)) {
  fs.mkdirSync(binDir, { recursive: true });
}
if (!fs.existsSync(distDir)) {
  fs.mkdirSync(distDir, { recursive: true });
}

// Create a placeholder file to ensure bin directory is included
const placeholderPath = path.join(binDir, '.gitkeep');
if (!fs.existsSync(placeholderPath)) {
  fs.writeFileSync(placeholderPath, '# This directory will contain the binary after installation\n');
}

// Create index.js for programmatic usage
const indexContent = `/**
 * devtool-mcp - MCP server for development tooling
 *
 * This package provides the devtool-mcp binary for use as an MCP server.
 *
 * Usage:
 *   Add to your MCP client configuration:
 *   {
 *     "mcpServers": {
 *       "devtool": {
 *         "command": "npx",
 *         "args": ["@anthropic/devtool-mcp"]
 *       }
 *     }
 *   }
 *
 * For more information, see:
 *   https://dev.standardbeagle.com/devtool-mcp/
 */

const path = require('path');
const { spawn } = require('child_process');

const BINARY_NAME = process.platform === 'win32' ? 'devtool-mcp.exe' : 'devtool-mcp';
const binaryPath = path.join(__dirname, '..', 'bin', BINARY_NAME);

/**
 * Get the path to the devtool-mcp binary.
 * @returns {string} Path to the binary
 */
function getBinaryPath() {
  return binaryPath;
}

/**
 * Run devtool-mcp with the given arguments.
 * @param {string[]} args - Command line arguments
 * @param {object} options - spawn options
 * @returns {ChildProcess}
 */
function run(args = [], options = {}) {
  return spawn(binaryPath, args, {
    stdio: 'inherit',
    ...options,
  });
}

module.exports = {
  getBinaryPath,
  run,
  binaryPath,
};
`;

fs.writeFileSync(path.join(distDir, 'index.js'), indexContent);

console.log('Pre-pack complete:');
console.log('  - Created bin/.gitkeep');
console.log('  - Created dist/index.js');
