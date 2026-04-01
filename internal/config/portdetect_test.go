//go:build !windows

package config

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestProcessNameByPID_Self(t *testing.T) {
	name := ProcessNameByPID(os.Getpid())
	assert.NotEmpty(t, name, "should return process name for own PID")
}

func TestProcessNameByPID_Invalid(t *testing.T) {
	name := ProcessNameByPID(-1)
	assert.Empty(t, name, "should return empty for invalid PID")
}

func TestProcessNameByPID_NonExistent(t *testing.T) {
	name := ProcessNameByPID(999999999)
	assert.Empty(t, name, "should return empty for non-existent PID")
}
