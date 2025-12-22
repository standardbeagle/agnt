import { execSync } from 'child_process';
import * as fs from 'fs';
import * as path from 'path';
import * as net from 'net';

const STATE_FILE = path.join(__dirname, '.test-state.json');

interface TestState {
  daemonStarted: boolean;
  proxyPort: number;
  socketPath: string;
}

/**
 * Send a command to the daemon via unix socket
 */
async function sendToDaemon(socketPath: string, command: string): Promise<string> {
  return new Promise((resolve) => {
    const socket = net.createConnection(socketPath);
    let response = '';

    socket.on('connect', () => {
      socket.write(command);
    });

    socket.on('data', (data) => {
      response += data.toString();
    });

    socket.on('end', () => {
      resolve(response);
    });

    socket.on('error', () => {
      resolve('error');
    });

    setTimeout(() => {
      socket.destroy();
      resolve(response || 'timeout');
    }, 5000);
  });
}

async function globalTeardown() {
  console.log('🧹 Cleaning up e2e test environment...');

  try {
    // Read state file to get socket path
    let socketPath = '/run/user/1000/devtool-mcp.sock';
    if (fs.existsSync(STATE_FILE)) {
      try {
        const state: TestState = JSON.parse(fs.readFileSync(STATE_FILE, 'utf-8'));
        socketPath = state.socketPath || socketPath;
      } catch {
        // Ignore parse errors
      }
    }

    // 1. Stop the proxy
    console.log('  Stopping proxy...');
    try {
      await sendToDaemon(socketPath, 'PROXY STOP e2e-test;;');
    } catch {
      console.log('  Proxy already stopped');
    }

    // 2. Clean up state file
    if (fs.existsSync(STATE_FILE)) {
      fs.unlinkSync(STATE_FILE);
    }

    // Note: We don't stop the daemon as it may be used by other processes

    console.log('✅ Teardown complete');
  } catch (err) {
    console.error('⚠️ Teardown warning:', err);
    // Don't throw - teardown should be best-effort
  }
}

export default globalTeardown;
