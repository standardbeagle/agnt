---
description: "Check for JavaScript errors in the browser"
allowed-tools: ["mcp__agnt__proxylog", "mcp__agnt__proxy"]
---

Check for JavaScript errors captured by the agnt proxy.

## Steps

1. Query the proxy logs for error entries:
   ```
   proxylog {proxy_id: "dev", types: ["error"], limit: 20}
   ```

2. If there are errors:
   - Summarize each error with its message, file, line, and column
   - Group errors by type if there are many
   - Suggest fixes for common error patterns

3. If no errors found:
   - Confirm the page is error-free
   - Suggest the user can trigger errors by interacting with the page

4. Optionally check for custom logs that might indicate issues:
   ```
   proxylog {proxy_id: "dev", types: ["custom"], limit: 10}
   ```

Note: The proxy ID "dev" is the default. If the user has a different proxy running, they should specify it.
