---
description: "Debug browser issues using agnt diagnostic tools"
allowed-tools: ["mcp__agnt__proxy", "mcp__agnt__proxylog", "mcp__agnt__currentpage"]
---

Comprehensive browser debugging using agnt diagnostic tools.

## Steps

1. Get an overview of the current page session:
   ```
   currentpage {proxy_id: "dev"}
   ```
   This shows active pages, resource counts, error counts, and interaction/mutation counts.

2. Check for JavaScript errors:
   ```
   proxylog {proxy_id: "dev", types: ["error"], limit: 10}
   ```

3. Check for recent user interactions (helps understand user flow):
   ```
   proxylog {proxy_id: "dev", types: ["interaction"], limit: 10}
   ```

4. Check for DOM mutations (helps debug dynamic UI issues):
   ```
   proxylog {proxy_id: "dev", types: ["mutation"], limit: 10}
   ```

5. If the user reported clicking something:
   ```
   proxy {action: "exec", id: "dev", code: "__devtool.interactions.getLastClickContext()"}
   ```
   This returns detailed context about the last click: element, ancestors, text content, and position.

6. To highlight recent DOM changes visually:
   ```
   proxy {action: "exec", id: "dev", code: "__devtool.mutations.highlightRecent(5000)"}
   ```
   This highlights elements that changed in the last 5 seconds.

7. For accessibility issues:
   ```
   proxy {action: "exec", id: "dev", code: "__devtool.auditAccessibility()"}
   ```

8. For CSS/layout issues:
   ```
   proxy {action: "exec", id: "dev", code: "__devtool.findOverflows()"}
   ```

9. To inspect a specific element:
   ```
   proxy {action: "exec", id: "dev", code: "__devtool.inspect('#element-selector')"}
   ```

Based on the findings, suggest fixes and explain the issues to the user.
