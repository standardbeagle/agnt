package tools

import (
	"strings"
	"testing"
)

// ruleCase is a single positive/negative test case for one hint rule.
type ruleCase struct {
	name       string
	js         string
	wantMatch  bool   // true = hint expected, false = no hint expected
	hintSubstr string // substring that must appear in the hint message
}

var hintRuleCases = []ruleCase{
	// getBoundingClientRect
	{
		name:       "getBoundingClientRect positive",
		js:         `var r = el.getBoundingClientRect(); console.log(r.top);`,
		wantMatch:  true,
		hintSubstr: "getPosition",
	},
	{
		name:      "getBoundingClientRect negative – benign code",
		js:        `var r = el.getClientRects(); console.log(r.length);`,
		wantMatch: false,
	},

	// getComputedStyle
	{
		name:       "getComputedStyle positive",
		js:         `var s = getComputedStyle(el); return s.color;`,
		wantMatch:  true,
		hintSubstr: "getComputed",
	},
	{
		name:      "getComputedStyle negative – benign code",
		js:        `var s = el.style.color; return s;`,
		wantMatch: false,
	},

	// querySelectorAll + .length
	{
		name:       "querySelectorAll.length positive",
		js:         `var n = document.querySelectorAll('div').length;`,
		wantMatch:  true,
		hintSubstr: "auditDOMComplexity",
	},
	{
		name:      "querySelectorAll.length negative – no .length",
		js:        `var els = document.querySelectorAll('div'); els.forEach(e => e.remove());`,
		wantMatch: false,
	},

	// tabindex
	{
		name:       "tabindex positive",
		js:         `el.setAttribute('tabindex', '-1');`,
		wantMatch:  true,
		hintSubstr: "getTabOrder",
	},
	{
		name:      "tabindex negative – benign code without tabindex",
		js:        `el.setAttribute('aria-hidden', 'true');`,
		wantMatch: false,
	},

	// addEventListener click
	{
		name:       "addEventListener click positive",
		js:         `el.addEventListener('click', handler);`,
		wantMatch:  true,
		hintSubstr: "interactions.getHistory",
	},
	{
		name:      "addEventListener click negative – different event",
		js:        `el.addEventListener('mouseover', handler);`,
		wantMatch: false,
	},

	// new MutationObserver
	{
		name:       "MutationObserver positive",
		js:         `var mo = new MutationObserver(cb); mo.observe(el, {childList: true});`,
		wantMatch:  true,
		hintSubstr: "mutations.getHistory",
	},
	{
		name:      "MutationObserver negative – benign code",
		js:        `var mo = new ResizeObserver(cb); mo.observe(el);`,
		wantMatch: false,
	},

	// contrast ratio math (0.2126)
	{
		name:       "luminance math positive",
		js:         `var lum = 0.2126 * r + 0.7152 * g + 0.0722 * b;`,
		wantMatch:  true,
		hintSubstr: "getContrast",
	},
	{
		name:      "luminance math negative – different coefficient",
		js:        `var lum = 0.299 * r + 0.587 * g + 0.114 * b;`,
		wantMatch: false,
	},

	// .innerHTML
	{
		name:       "innerHTML positive",
		js:         `var html = el.innerHTML;`,
		wantMatch:  true,
		hintSubstr: "captureDOM",
	},
	{
		name:      "innerHTML negative – benign code",
		js:        `var txt = el.innerText; return txt;`,
		wantMatch: false,
	},

	// .value in loop (form value gather)
	{
		name:       "form value positive",
		js:         `inputs.forEach(i => { data[i.name] = i.value; });`,
		wantMatch:  true,
		hintSubstr: "captureState",
	},
	{
		name:      "form value negative – no .value",
		js:        `inputs.forEach(i => { data[i.name] = i.textContent; });`,
		wantMatch: false,
	},

	// performance.getEntries
	{
		name:       "performance.getEntries positive",
		js:         `var entries = performance.getEntries();`,
		wantMatch:  true,
		hintSubstr: "captureNetwork",
	},
	{
		name:      "performance.getEntries negative – benign code",
		js:        `var t = performance.now(); return t;`,
		wantMatch: false,
	},
}

func TestScanForHints_RuleTable(t *testing.T) {
	for _, tc := range hintRuleCases {
		t.Run(tc.name, func(t *testing.T) {
			hints := ScanForHints(tc.js)
			if tc.wantMatch {
				if len(hints) == 0 {
					t.Fatalf("expected at least one hint, got none for JS: %q", tc.js)
				}
				found := false
				for _, h := range hints {
					if strings.Contains(h, tc.hintSubstr) {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("expected hint containing %q, got: %v", tc.hintSubstr, hints)
				}
			} else {
				if len(hints) != 0 {
					t.Fatalf("expected no hints, got: %v", hints)
				}
			}
		})
	}
}

func TestScanForHints_MultipleRules(t *testing.T) {
	// Snippet that triggers getBoundingClientRect + getComputedStyle + innerHTML.
	js := `
var rect = el.getBoundingClientRect();
var style = getComputedStyle(el);
var html = el.innerHTML;
`
	hints := ScanForHints(js)
	if len(hints) < 3 {
		t.Fatalf("expected at least 3 hints, got %d: %v", len(hints), hints)
	}
	must := []string{"getPosition", "getComputed", "captureDOM"}
	for _, sub := range must {
		found := false
		for _, h := range hints {
			if strings.Contains(h, sub) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected hint containing %q in %v", sub, hints)
		}
	}
}

func TestScanForHints_EmptyInput(t *testing.T) {
	hints := ScanForHints("")
	if len(hints) != 0 {
		t.Fatalf("expected no hints for empty input, got: %v", hints)
	}
}
