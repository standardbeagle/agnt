package replaytest

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkerBundleEmitsShimAndWorker(t *testing.T) {
	s := &Scenario{
		Name: "demo", BaseURL: "http://x",
		Recordings: []Recording{{Match: MatchKey{Method: "GET", Path: "/api/items"}, Status: 200, BodyRef: "b0", Hits: 1}},
		Blobs:      map[string]string{"b0": `{"ok":true}`},
	}
	js, err := GenerateBundle(s, "empty_array")
	require.NoError(t, err)
	assert.Contains(t, js, "window.fetch")
	assert.Contains(t, js, "XMLHttpRequest")
	assert.Contains(t, js, "__replay_miss")
	assert.Contains(t, js, `"b0"`)
	assert.Contains(t, js, "empty_array")
	assert.Contains(t, js, "Blob(")
	assert.True(t, strings.Count(js, "postMessage") >= 1)
}

func TestWorkerBundleNoPresetIsClean(t *testing.T) {
	s := &Scenario{Name: "d", Recordings: nil, Blobs: map[string]string{}}
	js, err := GenerateBundle(s, "")
	require.NoError(t, err)
	assert.Contains(t, js, `"activePreset":""`)
}

// TestWorkerStripsOriginBeforeMatching is the always-on source guard for the
// absolute-URL fix: buildKey must normalise through stripOrigin. Behaviour is
// proven by TestWorkerMatchesAbsoluteURLs under node.
func TestWorkerStripsOriginBeforeMatching(t *testing.T) {
	assert.Contains(t, workerJS, "function stripOrigin(u)")
	assert.Contains(t, workerJS, "rawURL=stripOrigin(rawURL);")
	assert.NotContains(t, workerJS, "`")
	assert.NotContains(t, workerJS, "%")
}

// TestWorkerMatchesAbsoluteURLs runs the shipped worker source under node and
// checks that every spelling of a request URL reaches the recording keyed by
// its path. Request.url is always absolute, so fetch(new Request(...)) depends
// on this.
func TestWorkerMatchesAbsoluteURLs(t *testing.T) {
	if os.Getenv("AGNT_JS_RUNTIME_TESTS") != "1" {
		t.Skip("SKIPPED: set AGNT_JS_RUNTIME_TESTS=1 to run the worker under node (source guard TestWorkerStripsOriginBeforeMatching still ran)")
	}
	const driver = `
var replies = [];
var self = { postMessage: function(m){ replies.push(m); } };
` + "%s" + `
var urls = process.argv.slice(2);
self.onmessage({data:{type:'init', payload:{activePreset:'', blobs:{b0:'hit'},
  recordings:[{match:{method:'GET', path:'/api/items/:id', query_keys:['q']}, status:200, body_ref:'b0', hits:urls.length}]}}});
urls.forEach(function(u, i){ self.onmessage({data:{type:'match', id:i, method:'get', url:u}}); });
process.stdout.write(replies.map(function(r){ return r.miss ? 'miss' : r.body; }).join(','));
`
	script := filepath.Join(t.TempDir(), "worker.js")
	require.NoError(t, os.WriteFile(script, []byte(strings.Replace(driver, "%s", workerJS, 1)), 0o644))

	cmd := exec.Command("node", script,
		"/api/items/42?q=1",
		"https://example.com/api/items/42?q=1",
		"http://localhost:3000/api/items/42?q=1&_=99#frag",
		"//cdn.example.com/api/items/42?q=1",
	)
	cmd.Dir = t.TempDir()
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	assert.Equal(t, "hit,hit,hit,hit", string(out))
}

// TestFetchShimNullBodyStatusSourceGuard is the always-on guard for the
// null-body fix: the fetch shim must not hand a body to the Response
// constructor for 204/205/304. Behaviour is proven under node by
// TestFetchShimReplaysNullBodyStatuses.
func TestFetchShimNullBodyStatusSourceGuard(t *testing.T) {
	assert.Contains(t, bundleTemplate, "function nullBodyStatus(s){ return s === 204 || s === 205 || s === 304; }")
	assert.Contains(t, bundleTemplate, "new Response(nullBodyStatus(r.status) ? null : r.body,")
}

// TestFetchShimReplaysNullBodyStatuses runs the shipped bundle (shim and worker)
// under node and replays recorded 204 and 304 responses through window.fetch.
// new Response("", {status: 204}) throws a TypeError, so before the fix every
// recorded DELETE-204 or revalidated GET-304 became a rejected fetch in the
// replayed app.
func TestFetchShimReplaysNullBodyStatuses(t *testing.T) {
	if os.Getenv("AGNT_JS_RUNTIME_TESTS") != "1" {
		t.Skip("SKIPPED: set AGNT_JS_RUNTIME_TESTS=1 to run the fetch shim under node (source guard TestFetchShimNullBodyStatusSourceGuard still ran)")
	}
	sc := &Scenario{
		Name: "nullbody",
		Recordings: []Recording{
			{Match: MatchKey{Method: "DELETE", Path: "/api/item"}, Status: 204, BodyRef: "b0", Hits: 1},
			{Match: MatchKey{Method: "PUT", Path: "/api/reset"}, Status: 205, BodyRef: "b0", Hits: 1},
			{Match: MatchKey{Method: "GET", Path: "/api/cached"}, Status: 304, BodyRef: "b0", Hits: 1},
			{Match: MatchKey{Method: "GET", Path: "/api/ok"}, Status: 200, BodyRef: "b1", Hits: 1},
		},
		Blobs: map[string]string{"b0": "", "b1": "ok"},
	}
	bundle, err := GenerateBundle(sc, "")
	require.NoError(t, err)
	workerSrc, err := json.Marshal(workerJS)
	require.NoError(t, err)

	// The Worker stub runs the shipped worker source in-process; messages hop
	// through setTimeout as they would through a real worker.
	driver := `
globalThis.window = globalThis;
var WORKER_SRC = ` + string(workerSrc) + `;
function Worker(){
  var outer = this;
  var wself = { postMessage: function(m){ setTimeout(function(){ outer.onmessage({data:m}); }, 0); } };
  (new Function('self', WORKER_SRC))(wself);
  this._w = wself;
}
Worker.prototype.postMessage = function(m){ var w = this._w; setTimeout(function(){ w.onmessage({data:m}); }, 0); };
globalThis.Worker = Worker;
` + bundle + `
var calls = [['DELETE','/api/item'], ['PUT','/api/reset'], ['GET','/api/cached'], ['GET','/api/ok']];
Promise.all(calls.map(function(c){
  return window.fetch(c[1], {method: c[0]}).then(function(res){
    return res.text().then(function(t){ return res.status + ':' + t; });
  }, function(e){ return 'rejected:' + e.message; });
})).then(function(out){ process.stdout.write(out.join(',')); });
`
	script := filepath.Join(t.TempDir(), "shim.js")
	require.NoError(t, os.WriteFile(script, []byte(driver), 0o644))

	cmd := exec.Command("node", script)
	cmd.Dir = t.TempDir()
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	assert.Equal(t, "204:,205:,304:,200:ok", string(out))
}

func TestSplitPathQueryBareOriginIsRoot(t *testing.T) {
	path, keys := splitPathQuery("https://example.com?q=1")
	assert.Equal(t, "/", path)
	assert.Equal(t, []string{"q"}, keys)
}
