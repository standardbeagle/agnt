package replaytest

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDOMSignatureNormalizesNoise(t *testing.T) {
	a := `<div>  Today   <span data-ts="123">x</span></div>`
	b := `<div>Today <span data-ts="999">x</span></div>`
	sigA := DOMSignature(a, []string{"data-ts"})
	sigB := DOMSignature(b, []string{"data-ts"})
	assert.Equal(t, sigA, sigB)

	c := `<div>Yesterday <span data-ts="1">x</span></div>`
	assert.NotEqual(t, sigA, DOMSignature(c, []string{"data-ts"}))
	assert.Contains(t, sigA, "blake3:")
}
