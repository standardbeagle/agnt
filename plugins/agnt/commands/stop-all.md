---
description: "Stop all running processes and proxies"
allowed-tools: ["mcp__devtool__proc", "mcp__devtool__proxy"]
---

Stop all running agnt processes and proxies.

## Steps

1. List all running processes:
   ```
   proc {action: "list", global: true}
   ```

2. List all running proxies:
   ```
   proxy {action: "list", global: true}
   ```

3. Stop each proxy first (to cleanly disconnect browsers):
   ```
   proxy {action: "stop", id: "<proxy_id>"}
   ```
   Repeat for each proxy.

4. Stop each process:
   ```
   proc {action: "stop", process_id: "<process_id>"}
   ```
   Repeat for each process.

5. Confirm to the user that all processes and proxies have been stopped.

Note: This uses `global: true` to include items from all directories, not just the current one.
