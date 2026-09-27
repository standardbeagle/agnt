// Dev OIDC persona chip for the floating indicator (chrome role).
//
// When the project declares a dev-oidc block, the proxy serves an issuer at
// /__agnt/oidc. This chip shows which persona the app is signed in as and
// switches it: choosing one submits the issuer's switch form INTO the content
// frame, so the persona cookie is set and the app is sent back through its
// own login. It is the same POST the `devauth` MCP tool and the overlay's
// `:as` command make.
//
// A proxy without dev-oidc answers /state with the app's own response (or an
// error); the chip then renders nothing.

(function() {
  'use strict';

  var PREFIX = '/__agnt/oidc';
  // Name given to the content frame when it has none, so a form can target it.
  var FRAME_TARGET = '__agnt_content';

  function contentFrame() {
    var ctx = window.__devtool_context;
    return ctx && ctx.contentFrame ? ctx.contentFrame() : null;
  }

  var theme = window.__devtoolTokens ? window.__devtoolTokens.theme() : {
    primary: '#6366f1', surface: '#ffffff', border: '#e2e8f0',
    text: '#1e293b', textMuted: '#64748b'
  };

  function fetchState() {
    return fetch(PREFIX + '/state', { credentials: 'same-origin', cache: 'no-store' })
      .then(function(resp) {
        var type = resp.headers.get('Content-Type') || '';
        if (!resp.ok || type.indexOf('application/json') !== 0) { return null; }
        return resp.json();
      })
      .then(function(st) {
        return st && Array.isArray(st.personas) && st.personas.length ? st : null;
      })
      .catch(function() { return null; });
  }

  // switchTo submits the switch form so that its response (a redirect to the
  // app's login path) loads in the content frame. Outside the wrapped shell
  // there is no content frame and the form targets this window.
  function switchTo(persona) {
    var frame = contentFrame();
    var form = document.createElement('form');
    form.method = 'POST';
    form.action = PREFIX + '/switch';
    if (frame) {
      if (!frame.name) { frame.name = FRAME_TARGET; }
      form.target = frame.name;
    }
    var input = document.createElement('input');
    input.type = 'hidden';
    input.name = 'persona';
    input.value = persona;
    form.appendChild(input);
    form.style.display = 'none';
    document.body.appendChild(form);
    form.submit();
    form.remove();
    return form;
  }

  function label(p) {
    return p.display_name || p.name;
  }

  function create() {
    var wrap = document.createElement('div');
    wrap.className = '__devtool-persona';
    wrap.style.cssText = 'position:relative;display:none;align-items:center;margin-left:auto;margin-right:4px;';

    var btn = document.createElement('button');
    btn.type = 'button';
    btn.className = '__devtool-persona-button';
    btn.setAttribute('aria-haspopup', 'menu');
    btn.setAttribute('aria-expanded', 'false');
    btn.style.cssText = 'font:inherit;font-size:11px;padding:2px 8px;border-radius:999px;cursor:pointer;' +
      'border:1px solid ' + theme.border + ';background:' + theme.surface + ';color:' + theme.text + ';white-space:nowrap;';
    wrap.appendChild(btn);

    var menu = document.createElement('div');
    menu.className = '__devtool-persona-menu';
    menu.setAttribute('role', 'menu');
    menu.hidden = true;
    menu.style.cssText = 'position:absolute;top:100%;right:0;margin-top:4px;min-width:180px;z-index:1;padding:4px;' +
      'border:1px solid ' + theme.border + ';border-radius:8px;background:' + theme.surface + ';box-shadow:0 4px 12px rgba(0,0,0,.15);';
    wrap.appendChild(menu);

    function setOpen(open) {
      menu.hidden = !open;
      btn.setAttribute('aria-expanded', open ? 'true' : 'false');
    }

    function render(st) {
      if (!st) {
        wrap.style.display = 'none';
        return;
      }
      wrap.style.display = 'flex';
      var current = null;
      st.personas.forEach(function(p) { if (p.name === st.persona) { current = p; } });
      btn.textContent = 'as: ' + (current ? label(current) : 'choose');
      btn.title = current ? (current.email + (current.roles && current.roles.length ? ' · ' + current.roles.join(', ') : '')) : 'Choose a dev-oidc persona';

      while (menu.firstChild) { menu.removeChild(menu.firstChild); }
      st.personas.forEach(function(p) {
        var item = document.createElement('button');
        item.type = 'button';
        item.setAttribute('role', 'menuitemradio');
        item.setAttribute('aria-checked', p.name === st.persona ? 'true' : 'false');
        item.dataset.persona = p.name;
        item.style.cssText = 'display:block;width:100%;text-align:left;font:inherit;font-size:12px;padding:6px 8px;border:0;' +
          'border-radius:6px;background:transparent;color:' + theme.text + ';cursor:pointer;';
        var name = document.createElement('div');
        name.textContent = label(p) + (p.name === st.persona ? ' ✓' : '');
        var meta = document.createElement('div');
        meta.style.cssText = 'color:' + theme.textMuted + ';font-size:11px;';
        meta.textContent = p.email + (p.roles && p.roles.length ? ' · ' + p.roles.join(', ') : '');
        item.appendChild(name);
        item.appendChild(meta);
        item.addEventListener('click', function(e) {
          e.stopPropagation();
          setOpen(false);
          switchTo(p.name);
          btn.textContent = 'as: ' + label(p) + '…';
        });
        menu.appendChild(item);
      });
    }

    btn.addEventListener('click', function(e) {
      e.stopPropagation();
      setOpen(menu.hidden);
    });
    menu.addEventListener('keydown', function(e) {
      if (e.key === 'Escape') { setOpen(false); btn.focus(); }
    });
    document.addEventListener('click', function() { setOpen(false); });

    var refresh = function() { return fetchState().then(render); };
    // The app signs in again inside the content frame after a switch, so
    // refresh whenever that frame finishes loading.
    var frame = contentFrame();
    if (frame) { frame.addEventListener('load', refresh); }
    wrap.__devtoolRefresh = refresh;
    refresh();
    return wrap;
  }

  window.__devtool_personaChip = { create: create, switchTo: switchTo, fetchState: fetchState };
})();
