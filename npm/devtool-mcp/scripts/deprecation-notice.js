#!/usr/bin/env node

const YELLOW = '\x1b[33m';
const CYAN = '\x1b[36m';
const RESET = '\x1b[0m';
const BOLD = '\x1b[1m';

console.log('');
console.log(`${YELLOW}${BOLD}╔════════════════════════════════════════════════════════════════╗${RESET}`);
console.log(`${YELLOW}${BOLD}║                                                                ║${RESET}`);
console.log(`${YELLOW}${BOLD}║  ⚠️  DEPRECATION NOTICE                                        ║${RESET}`);
console.log(`${YELLOW}${BOLD}║                                                                ║${RESET}`);
console.log(`${YELLOW}${BOLD}║  @standardbeagle/devtool-mcp has been renamed to:             ║${RESET}`);
console.log(`${YELLOW}${BOLD}║                                                                ║${RESET}`);
console.log(`${YELLOW}${BOLD}║      ${CYAN}@standardbeagle/agnt${YELLOW}                                    ║${RESET}`);
console.log(`${YELLOW}${BOLD}║                                                                ║${RESET}`);
console.log(`${YELLOW}${BOLD}║  Please update your installation:                             ║${RESET}`);
console.log(`${YELLOW}${BOLD}║                                                                ║${RESET}`);
console.log(`${YELLOW}${BOLD}║      npm uninstall @standardbeagle/devtool-mcp                ║${RESET}`);
console.log(`${YELLOW}${BOLD}║      npm install -g @standardbeagle/agnt                      ║${RESET}`);
console.log(`${YELLOW}${BOLD}║                                                                ║${RESET}`);
console.log(`${YELLOW}${BOLD}║  This wrapper package will continue to work but will          ║${RESET}`);
console.log(`${YELLOW}${BOLD}║  not receive updates. Switch to agnt for new features.        ║${RESET}`);
console.log(`${YELLOW}${BOLD}║                                                                ║${RESET}`);
console.log(`${YELLOW}${BOLD}╚════════════════════════════════════════════════════════════════╝${RESET}`);
console.log('');
