package daemon

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRetryBackoff(t *testing.T) {
	base := 5 * time.Second

	assert.Equal(t, base, retryBackoff(base, 1))
	assert.Equal(t, 2*base, retryBackoff(base, 2))
	assert.Equal(t, 4*base, retryBackoff(base, 3))

	// Corrupt persisted attempt counts must not panic the shift or produce a
	// past DeliverAt that re-fires the task instantly.
	assert.Equal(t, base, retryBackoff(base, 0))
	assert.Equal(t, base, retryBackoff(base, -7))

	// Large exponents clamp instead of overflowing int64 into a negative delay.
	for _, attempts := range []int{20, 63, 64, 1 << 20} {
		got := retryBackoff(base, attempts)
		assert.Equal(t, maxRetryBackoff, got, "attempts=%d", attempts)
		assert.Positive(t, got, "attempts=%d must never yield a non-positive backoff", attempts)
	}
}
