---
description: "Take a screenshot of the current browser page"
allowed-tools: ["mcp__agnt__proxy", "mcp__agnt__proxylog"]
---

Capture a screenshot from the proxied browser page.

## Steps

1. Execute the screenshot function in the browser:
   ```
   proxy {action: "exec", id: "dev", code: "__devtool.screenshot('screenshot')"}
   ```

2. Wait briefly for the screenshot to be captured and sent.

3. Query the logs for the screenshot entry:
   ```
   proxylog {proxy_id: "dev", types: ["screenshot"], limit: 1}
   ```

4. The screenshot log entry contains:
   - `image_path`: Path to the PNG file
   - `timestamp`: When the screenshot was taken

5. Report the screenshot path to the user. The image can be viewed with any image viewer.

Note: If there's no browser connected to the proxy, this will fail. Make sure the user has the proxy URL open in their browser.
