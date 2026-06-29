package replaytest

import (
	"encoding/json"
	"fmt"
)

// bundlePayload is the JSON handed to the in-page worker at boot.
type bundlePayload struct {
	Recordings   []Recording       `json:"recordings"`
	Blobs        map[string]string `json:"blobs"`
	ActivePreset string            `json:"activePreset"`
}

// GenerateBundle returns the full JavaScript (shim + worker) to inject for a
// replay session. Pure function: same inputs -> same output.
//
// The main-thread shim overrides window.fetch and window.XMLHttpRequest to
// delegate every request to a Web Worker (created from a Blob) that holds the
// recordings, the matcher queues, and the fuzz mutator. The worker source is
// embedded as a JSON-quoted JS string literal so it can be carried inside a Go
// raw-string template without backtick/escape collisions.
func GenerateBundle(s *Scenario, preset string) (string, error) {
	payload := bundlePayload{Recordings: s.Recordings, Blobs: s.Blobs, ActivePreset: preset}
	if payload.Blobs == nil {
		payload.Blobs = map[string]string{}
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	wsrc, err := json.Marshal(workerJS)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(bundleTemplate, string(data), string(wsrc)), nil
}

// bundleTemplate is the main-thread shim. First %s = boot payload JSON, second
// %s = JSON-quoted worker source string. Any literal percent must be %%.
const bundleTemplate = `(function(){
  var PAYLOAD = %s;
  var workerSrc = %s;
  var blob = new Blob([workerSrc], {type:'application/javascript'});
  var worker = new Worker(URL.createObjectURL(blob));
  worker.postMessage({type:'init', payload: PAYLOAD});

  var pending = {}, seq = 0;
  worker.onmessage = function(e){
    var m = e.data;
    if(m && m.type === 'reply'){
      var cb = pending[m.id];
      if(cb){ delete pending[m.id]; cb(m); }
    }
  };

  function sigOf(body){
    var c = (window.crypto || self.crypto);
    if(!body || !c || !c.subtle) return Promise.resolve('');
    var bytes = new TextEncoder().encode(body);
    return c.subtle.digest('SHA-256', bytes).then(function(buf){
      var a = new Uint8Array(buf), s = '';
      for(var i=0;i<8;i++){ var h = a[i].toString(16); if(h.length < 2){ h = '0'+h; } s += h; }
      return s;
    });
  }

  function ask(method, url, body){
    return sigOf(typeof body === 'string' ? body : '').then(function(sig){
      return new Promise(function(resolve){
        var id = ++seq;
        pending[id] = resolve;
        worker.postMessage({type:'match', id:id, method:method, url:url, bodySig:sig});
      });
    });
  }

  var origFetch = window.fetch;
  window.fetch = function(input, init){
    var url = (typeof input === 'string') ? input : (input && input.url) || '';
    var method = (init && init.method) || (input && input.method) || 'GET';
    var body = (init && init.body) || '';
    return ask(method, url, typeof body === 'string' ? body : '').then(function(r){
      if(r.miss){
        return new Response('{"__replay_miss":true}', {status:599, headers:{'content-type':'application/json'}});
      }
      return new Response(r.body, {status:r.status, headers:r.headers||{}});
    });
  };
  window.fetch.__replay_orig = origFetch;

  var OrigXHR = window.XMLHttpRequest;
  function ReplayXHR(){
    this.readyState = 0;
    this.status = 0;
    this.responseText = '';
    this.response = '';
    this.onreadystatechange = null;
    this.onload = null;
    this._method = 'GET';
    this._url = '';
  }
  ReplayXHR.prototype.open = function(method, url){
    this._method = method || 'GET';
    this._url = url || '';
    this.readyState = 1;
    if(this.onreadystatechange) this.onreadystatechange();
  };
  ReplayXHR.prototype.setRequestHeader = function(){};
  ReplayXHR.prototype.getAllResponseHeaders = function(){ return ''; };
  ReplayXHR.prototype.send = function(body){
    var self = this;
    ask(self._method, self._url, typeof body === 'string' ? body : '').then(function(r){
      if(r.miss){
        self.status = 599;
        self.responseText = '{"__replay_miss":true}';
      } else {
        self.status = r.status;
        self.responseText = r.body;
      }
      self.response = self.responseText;
      self.readyState = 4;
      if(self.onreadystatechange) self.onreadystatechange();
      if(self.onload) self.onload();
    });
  };
  ReplayXHR.prototype.abort = function(){};
  ReplayXHR.__replay_orig = OrigXHR;
  window.XMLHttpRequest = ReplayXHR;

  window.__replay_active = true;
})();`

// workerJS is the Web Worker source. It MUST contain no backtick and no literal
// percent character (so the JSON-quoted form is safe inside fmt.Sprintf). It
// mirrors match.go's recKey/buildKey and the six fuzz presets from fuzz.go.
const workerJS = `
var QUEUES = {}, BLOBS = {}, PRESET = '';
function templatePath(p){ return p.split('/').map(function(s){
  return (/^([0-9]+|[0-9a-zA-Z-]{12,})$/.test(s)) ? ':id' : s; }).join('/'); }
function buildKey(method, rawURL, bodySig){
  var path=rawURL, query='';
  var i=rawURL.indexOf('?'); if(i>=0){ path=rawURL.slice(0,i); query=rawURL.slice(i+1); }
  path=templatePath(path);
  var keys=[];
  if(query){ query.split('&').forEach(function(kv){ var k=kv.split('=')[0]; if(k && k!=='_') keys.push(k); }); }
  keys.sort();
  var key=method.toUpperCase()+' '+path;
  if(keys.length) key+=' ?'+keys.join(',');
  if(bodySig) key+=' #'+bodySig;
  return key;
}
function recKey(r){
  var keys=(r.match.query_keys||[]).slice().sort();
  var key=r.match.method.toUpperCase()+' '+templatePath(r.match.path);
  if(keys.length) key+=' ?'+keys.join(',');
  if(r.request_body_sig) key+=' #'+r.request_body_sig;
  return key;
}
function mutate(status, body){
  if(!PRESET) return {status:status, body:body};
  try {
    if(PRESET==='http_error') return {status:500, body:'{"error":"injected"}'};
    if(PRESET==='truncated_json') return {status:status, body: body.slice(0, Math.max(1, body.length/2|0))};
    var v=JSON.parse(body);
    if(PRESET==='empty_array'){ v=(function e(x){ if(Array.isArray(x)) return []; if(x&&typeof x==='object'){ for(var k in x) x[k]=e(x[k]); } return x; })(v); }
    if(PRESET==='null_fields'){ if(v&&typeof v==='object'){ for(var k in v) v[k]=null; } }
    if(PRESET==='reordered'&&Array.isArray(v)) v.reverse();
    if(PRESET==='type_flip'&&v&&typeof v==='object'){ for(var k in v){ if(typeof v[k]==='number') v[k]='flipped'; else if(typeof v[k]==='string') v[k]=v[k].length; } }
    return {status:status, body:JSON.stringify(v)};
  } catch(e){ return {status:status, body:body}; }
}
self.onmessage=function(e){
  var m=e.data;
  if(m.type==='init'){
    PRESET=m.payload.activePreset||''; BLOBS=m.payload.blobs||{};
    (m.payload.recordings||[]).forEach(function(r){
      var k=recKey(r); var n=r.hits||1; QUEUES[k]=QUEUES[k]||[];
      for(var i=0;i<n;i++) QUEUES[k].push(r);
    });
    return;
  }
  if(m.type==='match'){
    var k=buildKey(m.method, m.url, m.bodySig||'');
    var q=QUEUES[k];
    if(!q||!q.length){ self.postMessage({type:'reply', id:m.id, miss:true}); return; }
    var r=q.shift();
    var body=BLOBS[r.body_ref]||'';
    var mut=mutate(r.status, body);
    self.postMessage({type:'reply', id:m.id, status:mut.status, body:mut.body, headers:r.headers||{}});
  }
};`
