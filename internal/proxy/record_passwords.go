package proxy

import "strings"

// Password field values in browser telemetry.
//
// The injected tracking records what is typed into form fields (interaction
// `input` values, value-attribute mutations). A password field's value is
// redacted by default. The operator can approve recording it for one proxy's
// dev app with `record-passwords true` in that proxy's .agnt.kdl block (test
// logins on a local dev server). Two limits apply whatever the config says:
//
//   - The page is told to record (window.__devtool_record_passwords) only
//     while no public URL is bound to the proxy, so a page served through a
//     tunnel does not send them at all.
//   - The proxy keeps a password value only from a trusted connection
//     (controlCallerTrusted) and only while the opt-in is in effect. A
//     visitor runs their own copy of the page, so the browser's choice is not
//     the control: every password-marked event from any other connection is
//     redacted here, before it is logged or tracked.

// redactedPasswordValue replaces a password value the proxy does not keep.
// The same text mutation.js uses, so a redaction reads the same wherever it
// happened.
const redactedPasswordValue = "[redacted]"

// redactedPasswordHTML replaces the markup of an added or removed node that
// still holds a password value. The markup is dropped whole: it is truncated
// in the browser, so a tag can be cut mid-attribute, and no rewrite of a
// fragment could promise to find every value in it.
const redactedPasswordHTML = "[redacted: markup held a password field value]"

// RecordPasswords reports whether the operator enabled record-passwords for
// this proxy.
func (ps *ProxyServer) RecordPasswords() bool {
	return ps.recordPasswords
}

// recordPasswordsInEffect is the opt-in narrowed by the exposure posture: off
// while any public URL (a tunnel, a named tunnel, a configured public-url) is
// bound to the proxy.
func (ps *ProxyServer) recordPasswordsInEffect() bool {
	return ps.recordPasswords && ps.GetPublicURL() == ""
}

// recordPasswordsJS is the inline page-config script that tells the injected
// tracking to record password values, or "" when it must not.
func (ps *ProxyServer) recordPasswordsJS() string {
	if !ps.recordPasswordsInEffect() {
		return ""
	}
	return "window.__devtool_record_passwords=true;"
}

// keepPasswordValues reports whether password values this connection sends
// are kept.
func (c *wsConnState) keepPasswordValues() bool {
	return c.trusted && c.ps.recordPasswordsInEffect()
}

// redactPasswordValues drops the value and key of an interaction on a
// password field. The field is identified by the browser's password marker or
// by the target's recorded type attribute.
func (e *InteractionEvent) redactPasswordValues() {
	if !e.Password && !strings.EqualFold(e.Target.Attributes["type"], "password") {
		return
	}
	if e.Value != "" {
		e.Value = redactedPasswordValue
	}
	if e.Key != nil {
		e.Key = &KeyboardInfo{Masked: true}
	}
}

// redactPasswordValues drops the old and new value of a password field's
// value-attribute change, and the markup of an added or removed node that
// still holds a password value.
func (m *MutationEvent) redactPasswordValues() {
	for _, nodes := range [][]MutationNode{m.Added, m.Removed} {
		for i := range nodes {
			if nodes[i].Password && nodes[i].HTML != "" {
				nodes[i].HTML = redactedPasswordHTML
			}
		}
	}
	a := m.Attribute
	if a == nil || !a.Password {
		return
	}
	if a.OldValue != "" {
		a.OldValue = redactedPasswordValue
	}
	if a.NewValue != "" {
		a.NewValue = redactedPasswordValue
	}
}
