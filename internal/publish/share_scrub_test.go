package publish

import (
	"bufio"
	"net/http"
	"strings"
	"testing"
)

// TestScrubSharePath_AbsoluteForm: a request sent in absolute-form gives r.URL
// a scheme and host, so the logged r.URL.String() starts with "http://host".
// The token must still be scrubbed to hash[:8] (INV-9).
func TestScrubSharePath_AbsoluteForm(t *testing.T) {
	const token = "Zm9vYmFyYmF6cXV4LXNlY3JldC10b2tlbi1hYmNkZWY"
	prefix := hashToken(token)[:8]

	req, err := http.ReadRequest(bufio.NewReader(strings.NewReader(
		"GET http://dev.example:8080/s/" + token + "/feedback?x=1 HTTP/1.1\r\nHost: dev.example:8080\r\n\r\n")))
	if err != nil {
		t.Fatalf("ReadRequest: %v", err)
	}
	logged := req.URL.String()
	if !strings.HasPrefix(logged, "http://dev.example:8080/s/"+token) {
		t.Fatalf("premise broken: absolute-form r.URL.String() = %q", logged)
	}

	cases := map[string]string{
		logged:                         "http://dev.example:8080/s/" + prefix + "/feedback?x=1",
		"https://u:p@h/s/" + token:     "https://u:p@h/s/" + prefix,
		"http://h/s/" + token + "?q=1": "http://h/s/" + prefix + "?q=1",
		"//h/s/" + token + "/sub":      "//h/s/" + prefix + "/sub",
		"/s/" + token:                  "/s/" + prefix,
		"http://h/api/s/" + token:      "http://h/api/s/" + token, // not a share path
		"http://h":                     "http://h",
		"http://h?next=/s/" + token:    "http://h?next=/s/" + token, // query, not path
		"mailto:x":                     "mailto:x",
		"relative/s/" + token:          "relative/s/" + token,
	}
	for in, want := range cases {
		if got := ScrubSharePath(in); got != want {
			t.Errorf("ScrubSharePath(%q) = %q, want %q", in, got, want)
		}
	}
	if got := ScrubSharePath(logged); strings.Contains(got, token) {
		t.Fatalf("absolute-form log URL still carries the token: %q", got)
	}
}
