import { execSync, spawn, ChildProcess } from 'child_process';
import * as fs from 'fs';
import * as path from 'path';
import * as net from 'net';

const PROJECT_ROOT = path.resolve(__dirname, '..');
const AGNT_BINARY = path.join(PROJECT_ROOT, 'agnt');
const FIXTURE_PORT = 8765;
const PROXY_PORT = 12345;

// Get the socket path from the system
function getSocketPath(): string {
  // Try XDG_RUNTIME_DIR first (Linux)
  const runtimeDir = process.env.XDG_RUNTIME_DIR;
  if (runtimeDir) {
    return path.join(runtimeDir, 'devtool-mcp.sock');
  }
  // Fallback to /tmp
  return `/tmp/devtool-mcp-${process.env.USER || 'user'}.sock`;
}

const SOCKET_PATH = getSocketPath();

// State file to share between setup and teardown
const STATE_FILE = path.join(__dirname, '.test-state.json');

interface TestState {
  daemonStarted: boolean;
  proxyPort: number;
  socketPath: string;
}

/**
 * Send a command to the daemon via unix socket
 */
async function sendToDaemon(command: string): Promise<string> {
  return new Promise((resolve, reject) => {
    const socket = net.createConnection(SOCKET_PATH);
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

    socket.on('error', (err) => {
      reject(err);
    });

    // Timeout after 5 seconds
    setTimeout(() => {
      socket.destroy();
      resolve(response || 'timeout');
    }, 5000);
  });
}

/**
 * Wait for port to become available
 */
async function waitForPort(port: number, timeout = 10000): Promise<boolean> {
  const start = Date.now();
  while (Date.now() - start < timeout) {
    try {
      await new Promise<void>((resolve, reject) => {
        const socket = net.createConnection(port, 'localhost');
        socket.once('connect', () => {
          socket.destroy();
          resolve();
        });
        socket.once('error', reject);
      });
      return true;
    } catch {
      await new Promise((r) => setTimeout(r, 500));
    }
  }
  return false;
}

async function globalSetup() {
  console.log('🚀 Setting up e2e test environment...');
  console.log(`  Socket path: ${SOCKET_PATH}`);

  const state: TestState = {
    daemonStarted: false,
    proxyPort: PROXY_PORT,
    socketPath: SOCKET_PATH,
  };

  try {
    // 1. Ensure agnt daemon is running
    console.log('  Starting daemon...');
    try {
      execSync(`${AGNT_BINARY} daemon start`, {
        stdio: 'pipe',
        timeout: 10000,
      });
      state.daemonStarted = true;
      console.log('  Daemon started');
    } catch {
      console.log('  Daemon already running or failed to start');
    }

    // Wait for socket to be ready
    await new Promise((r) => setTimeout(r, 1000));

    // Check if socket exists
    if (!fs.existsSync(SOCKET_PATH)) {
      console.log(`  Warning: Socket not found at ${SOCKET_PATH}`);
      // Try to find the socket
      const altPaths = [
        `/run/user/${process.getuid?.() || 1000}/devtool-mcp.sock`,
        `/tmp/devtool-mcp-${process.env.USER}.sock`,
        '/tmp/devtool-mcp.sock',
      ];
      for (const p of altPaths) {
        if (fs.existsSync(p)) {
          console.log(`  Found socket at ${p}`);
          (SOCKET_PATH as any) = p;
          state.socketPath = p;
          break;
        }
      }
    }

    // 2. Start proxy pointing to fixture server
    console.log(`  Starting proxy on port ${PROXY_PORT}...`);
    try {
      const response = await sendToDaemon(
        `PROXY START e2e-test http://localhost:${FIXTURE_PORT} ${PROXY_PORT};;`
      );
      console.log(`  Proxy response: ${response.trim().substring(0, 100)}`);

      // Wait for proxy to be ready
      const proxyReady = await waitForPort(PROXY_PORT, 10000);
      if (proxyReady) {
        console.log('  Proxy ready');
      } else {
        console.warn('  Warning: Proxy may not be fully ready');
      }
    } catch (err) {
      console.error('  Failed to start proxy:', err);
    }

    // Save state for tests and teardown
    fs.writeFileSync(STATE_FILE, JSON.stringify(state, null, 2));

    console.log('✅ Setup complete');
  } catch (err) {
    console.error('❌ Setup failed:', err);
    throw err;
  }
}

export default globalSetup;
