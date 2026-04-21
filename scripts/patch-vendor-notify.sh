#!/usr/bin/env bash
# Patch vendored MCP SDK to add ServerSession.Notify method.
# Required until upstream merges PR #898 (github.com/modelcontextprotocol/go-sdk#898).
# Run after every `go mod vendor` invocation.
set -e
FILE="vendor/github.com/modelcontextprotocol/go-sdk/mcp/server.go"

if grep -q 'func (ss \*ServerSession) Notify(' "$FILE"; then
  echo "Already patched"
  exit 0
fi

python3 -c "
with open('$FILE') as f: c = f.read()
if '\"strings\"' not in c:
    c = c.replace('\"slices\"', '    \"slices\"\n    \"strings\"')
notify = '''
// Notify sends a notification with an arbitrary custom method name.
// method MUST start with \"notifications/\".
func (ss *ServerSession) Notify(ctx context.Context, method string, params any) error {
\tif !strings.HasPrefix(method, \"notifications/\") {
\t\treturn fmt.Errorf(\"Notify: method must start with notifications/, got %q\", method)
\t}
\treturn ss.getConn().Notify(ctx, method, params)
}
'''
c = c.replace('// NotifyProgress', notify + '// NotifyProgress')
with open('$FILE', 'w') as f: f.write(c)
print('Patched vendor with ServerSession.Notify')
"
