package proxy

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

type codeSpy struct {
	http.ResponseWriter
	codes []int
}

func (s *codeSpy) WriteHeader(c int) { s.codes = append(s.codes, c) }

// A 1xx informational status (103 Early Hints) must not stand in for the final
// status: the recorder logs the real one and the client receives both.
func TestResponseRecorder_InformationalStatusIsNotFinal(t *testing.T) {
	spy := &codeSpy{ResponseWriter: httptest.NewRecorder()}
	rr := &responseRecorder{ResponseWriter: spy, body: &bytes.Buffer{}}

	rr.WriteHeader(http.StatusEarlyHints)
	if rr.wroteHeader {
		t.Fatal("103 ended the header phase")
	}
	rr.WriteHeader(http.StatusNotFound)
	if rr.statusCode != http.StatusNotFound {
		t.Fatalf("recorded status = %d, want 404", rr.statusCode)
	}
	if want := []int{103, 404}; !reflect.DeepEqual(spy.codes, want) {
		t.Fatalf("forwarded codes = %v, want %v", spy.codes, want)
	}
}
