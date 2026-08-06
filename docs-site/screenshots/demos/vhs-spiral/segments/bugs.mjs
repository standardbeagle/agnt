// Bug states for the vhs-spiral demo. Each export is a self-contained function
// evaluated inside the page (raw upstream or proxied content frame) — no
// closures, no imports.
//
// The "bug": the Add-customer form's submit button rendered half off the right
// edge of the viewport. Attempt 2's blind "fix" makes it worse (gone entirely).

// Half off-screen: right ~half of the button clipped by the viewport edge.
export const halfOff = () => {
  const b = document.getElementById('f-submit');
  b.style.position = 'fixed';
  b.style.left = 'calc(100vw - 80px)';
  b.style.bottom = '48px';
  b.style.zIndex = '9999';
};

// Worse: the button is gone entirely (attempt 2's "position: absolute" guess).
export const gone = () => {
  const b = document.getElementById('f-submit');
  b.style.position = 'fixed';
  b.style.left = '110vw';
  b.style.bottom = '48px';
};

// Measure the clipping honestly: returns the numbers the fix segment shows.
export const measure = () => {
  const b = document.getElementById('f-submit');
  const r = b.getBoundingClientRect();
  return {
    left: Math.round(r.left),
    right: Math.round(r.right),
    viewport: window.innerWidth,
    clippedPx: Math.max(0, Math.round(r.right - window.innerWidth)),
  };
};

// The one-pass fix: undo the broken positioning.
export const fix = () => {
  const b = document.getElementById('f-submit');
  b.style.position = '';
  b.style.left = '';
  b.style.bottom = '';
  b.style.zIndex = '';
  b.style.outline = '';
};
