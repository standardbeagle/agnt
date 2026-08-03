package tools

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildAuditOptions(t *testing.T) {
	tests := []struct {
		name     string
		input    ResponsiveAuditInput
		expected map[string]any
	}{
		{
			name:  "empty input uses defaults",
			input: ResponsiveAuditInput{},
			expected: map[string]any{
				"raw": false,
			},
		},
		{
			name: "custom viewports",
			input: ResponsiveAuditInput{
				Viewports: []ViewportInput{
					{Name: "custom", Width: 500, Height: 800},
				},
			},
			expected: map[string]any{
				"viewports": []map[string]any{
					{"name": "custom", "width": 500, "height": 800},
				},
				"raw": false,
			},
		},
		{
			name: "custom checks",
			input: ResponsiveAuditInput{
				Checks: []string{"layout", "overflow"},
			},
			expected: map[string]any{
				"checks": []string{"layout", "overflow"},
				"raw":    false,
			},
		},
		{
			name: "raw mode",
			input: ResponsiveAuditInput{
				Raw: true,
			},
			expected: map[string]any{
				"raw": true,
			},
		},
		{
			name: "custom timeout",
			input: ResponsiveAuditInput{
				Timeout: 20000,
			},
			expected: map[string]any{
				"timeout": 20000,
				"raw":     false,
			},
		},
		{
			name: "all options",
			input: ResponsiveAuditInput{
				Viewports: []ViewportInput{
					{Name: "xs", Width: 320, Height: 568},
					{Name: "md", Width: 768, Height: 1024},
				},
				Checks:  []string{"layout"},
				Timeout: 15000,
				Raw:     true,
			},
			expected: map[string]any{
				"viewports": []map[string]any{
					{"name": "xs", "width": 320, "height": 568},
					{"name": "md", "width": 768, "height": 1024},
				},
				"checks":  []string{"layout"},
				"timeout": 15000,
				"raw":     true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildAuditOptions(tt.input)

			// Check raw
			if raw, ok := result["raw"].(bool); !ok || raw != tt.expected["raw"].(bool) {
				t.Errorf("raw mismatch: got %v, want %v", raw, tt.expected["raw"])
			}

			// Check timeout
			if tt.input.Timeout > 0 {
				if timeout, ok := result["timeout"].(int); !ok || timeout != tt.expected["timeout"].(int) {
					t.Errorf("timeout mismatch: got %v, want %v", timeout, tt.expected["timeout"])
				}
			}

			// Check checks
			if len(tt.input.Checks) > 0 {
				if checks, ok := result["checks"].([]string); !ok {
					t.Error("checks not found in result")
				} else {
					for i, c := range checks {
						if c != tt.expected["checks"].([]string)[i] {
							t.Errorf("checks[%d] mismatch: got %v, want %v", i, c, tt.expected["checks"].([]string)[i])
						}
					}
				}
			}

			// Check viewports
			if len(tt.input.Viewports) > 0 {
				if viewports, ok := result["viewports"].([]map[string]any); !ok {
					t.Error("viewports not found in result")
				} else {
					expectedVPs := tt.expected["viewports"].([]map[string]any)
					if len(viewports) != len(expectedVPs) {
						t.Errorf("viewports count mismatch: got %d, want %d", len(viewports), len(expectedVPs))
					}
					for i, vp := range viewports {
						if vp["name"] != expectedVPs[i]["name"] {
							t.Errorf("viewport[%d] name mismatch: got %v, want %v", i, vp["name"], expectedVPs[i]["name"])
						}
						if vp["width"] != expectedVPs[i]["width"] {
							t.Errorf("viewport[%d] width mismatch: got %v, want %v", i, vp["width"], expectedVPs[i]["width"])
						}
						if vp["height"] != expectedVPs[i]["height"] {
							t.Errorf("viewport[%d] height mismatch: got %v, want %v", i, vp["height"], expectedVPs[i]["height"])
						}
					}
				}
			}
		})
	}
}

func TestResponsiveAuditInputSchema(t *testing.T) {
	t.Run("empty input", func(t *testing.T) {
		input := ResponsiveAuditInput{}
		assert.Empty(t, input.ProxyID)
		assert.Nil(t, input.Viewports)
		assert.Nil(t, input.Checks)
		assert.Zero(t, input.Timeout)
		assert.False(t, input.Raw)
	})

	t.Run("full input", func(t *testing.T) {
		input := ResponsiveAuditInput{
			ProxyID: "dev",
			Viewports: []ViewportInput{
				{Name: "mobile", Width: 375, Height: 667},
				{Name: "desktop", Width: 1440, Height: 900},
			},
			Checks:  []string{"layout", "overflow"},
			Timeout: 15000,
			Raw:     true,
		}

		assert.Equal(t, "dev", input.ProxyID)
		require.Len(t, input.Viewports, 2)
		assert.Equal(t, "mobile", input.Viewports[0].Name)
		assert.Equal(t, 375, input.Viewports[0].Width)
		assert.Equal(t, 667, input.Viewports[0].Height)
		assert.Equal(t, []string{"layout", "overflow"}, input.Checks)
		assert.Equal(t, 15000, input.Timeout)
		assert.True(t, input.Raw)
	})
}

func TestViewportInput(t *testing.T) {
	t.Run("create viewport", func(t *testing.T) {
		vp := ViewportInput{
			Name:   "tablet",
			Width:  768,
			Height: 1024,
		}

		assert.Equal(t, "tablet", vp.Name)
		assert.Equal(t, 768, vp.Width)
		assert.Equal(t, 1024, vp.Height)
	})
}

func TestResponsiveAuditOutput(t *testing.T) {
	t.Run("compact output", func(t *testing.T) {
		output := ResponsiveAuditOutput{
			Summary: "MOBILE (375px) - 2 issues\n  ! [layout] .header - collapsed content\n  o [overflow] .sidebar - truncated text",
		}

		assert.NotEmpty(t, output.Summary)
		assert.Nil(t, output.Raw)
	})

	t.Run("raw JSON output", func(t *testing.T) {
		rawData := map[string]any{
			"viewports": map[string]any{
				"mobile": map[string]any{
					"width": 375,
					"issues": []any{
						map[string]any{
							"type":     "layout",
							"severity": "critical",
							"message":  "collapsed content",
						},
					},
				},
			},
			"summary": map[string]any{
				"total":    1,
				"critical": 1,
				"minor":    0,
			},
		}

		output := ResponsiveAuditOutput{
			Summary: `{"viewports":{"mobile":{"width":375,"issues":[{"type":"layout","severity":"critical","message":"collapsed content"}]}},"summary":{"total":1,"critical":1,"minor":0}}`,
			Raw:     rawData,
		}

		assert.NotEmpty(t, output.Summary)
		assert.NotNil(t, output.Raw)
	})
}

func TestBuildAuditOptionsJSON(t *testing.T) {
	t.Run("options are JSON serializable", func(t *testing.T) {
		input := ResponsiveAuditInput{
			Viewports: []ViewportInput{
				{Name: "custom", Width: 500, Height: 800},
			},
			Checks:  []string{"layout"},
			Timeout: 20000,
			Raw:     true,
		}

		opts := buildAuditOptions(input)

		// Should be JSON serializable
		data, err := json.Marshal(opts)
		require.NoError(t, err)

		// Should be able to unmarshal back
		var unmarshaled map[string]any
		err = json.Unmarshal(data, &unmarshaled)
		require.NoError(t, err)

		assert.Equal(t, true, unmarshaled["raw"])
	})

	t.Run("empty options JSON", func(t *testing.T) {
		input := ResponsiveAuditInput{}
		opts := buildAuditOptions(input)

		data, err := json.Marshal(opts)
		require.NoError(t, err)

		var unmarshaled map[string]any
		err = json.Unmarshal(data, &unmarshaled)
		require.NoError(t, err)

		assert.Equal(t, false, unmarshaled["raw"])
	})
}

func TestBuildAuditOptionsEdgeCases(t *testing.T) {
	t.Run("nil slices", func(t *testing.T) {
		input := ResponsiveAuditInput{
			Viewports: nil,
			Checks:    nil,
		}

		opts := buildAuditOptions(input)

		// Nil slices should not be included
		_, hasViewports := opts["viewports"]
		_, hasChecks := opts["checks"]

		assert.False(t, hasViewports)
		assert.False(t, hasChecks)
	})

	t.Run("empty slices", func(t *testing.T) {
		input := ResponsiveAuditInput{
			Viewports: []ViewportInput{},
			Checks:    []string{},
		}

		opts := buildAuditOptions(input)

		// Empty slices should not be included (len check)
		_, hasViewports := opts["viewports"]
		_, hasChecks := opts["checks"]

		assert.False(t, hasViewports)
		assert.False(t, hasChecks)
	})

	t.Run("zero timeout excluded", func(t *testing.T) {
		input := ResponsiveAuditInput{
			Timeout: 0,
		}

		opts := buildAuditOptions(input)

		_, hasTimeout := opts["timeout"]
		assert.False(t, hasTimeout)
	})

	t.Run("negative timeout excluded", func(t *testing.T) {
		input := ResponsiveAuditInput{
			Timeout: -1,
		}

		opts := buildAuditOptions(input)

		_, hasTimeout := opts["timeout"]
		assert.False(t, hasTimeout)
	})

	t.Run("multiple viewports", func(t *testing.T) {
		input := ResponsiveAuditInput{
			Viewports: []ViewportInput{
				{Name: "xs", Width: 320, Height: 568},
				{Name: "sm", Width: 375, Height: 667},
				{Name: "md", Width: 768, Height: 1024},
				{Name: "lg", Width: 1024, Height: 768},
				{Name: "xl", Width: 1440, Height: 900},
			},
		}

		opts := buildAuditOptions(input)

		viewports, ok := opts["viewports"].([]map[string]any)
		require.True(t, ok)
		require.Len(t, viewports, 5)
		assert.Equal(t, "xs", viewports[0]["name"])
		assert.Equal(t, "xl", viewports[4]["name"])
	})

	t.Run("all check types", func(t *testing.T) {
		input := ResponsiveAuditInput{
			Checks: []string{"layout", "overflow", "a11y"},
		}

		opts := buildAuditOptions(input)

		checks, ok := opts["checks"].([]string)
		require.True(t, ok)
		assert.Equal(t, []string{"layout", "overflow", "a11y"}, checks)
	})
}

func TestViewportInputValidation(t *testing.T) {
	t.Run("zero dimensions", func(t *testing.T) {
		vp := ViewportInput{Name: "zero", Width: 0, Height: 0}
		assert.Equal(t, 0, vp.Width)
		assert.Equal(t, 0, vp.Height)
	})

	t.Run("very large dimensions", func(t *testing.T) {
		vp := ViewportInput{Name: "large", Width: 10000, Height: 10000}
		assert.Equal(t, 10000, vp.Width)
		assert.Equal(t, 10000, vp.Height)
	})

	t.Run("empty name", func(t *testing.T) {
		vp := ViewportInput{Name: "", Width: 375, Height: 667}
		assert.Empty(t, vp.Name)
	})

	t.Run("special characters in name", func(t *testing.T) {
		vp := ViewportInput{Name: "custom-viewport_v2", Width: 375, Height: 667}
		assert.Equal(t, "custom-viewport_v2", vp.Name)
	})
}

func TestValidateResponsiveAuditInput(t *testing.T) {
	tests := []struct {
		name    string
		input   ResponsiveAuditInput
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid input",
			input:   ResponsiveAuditInput{Checks: []string{"layout", "overflow"}},
			wantErr: false,
		},
		{
			name:    "invalid check type",
			input:   ResponsiveAuditInput{Checks: []string{"invalid"}},
			wantErr: true,
			errMsg:  "invalid check type",
		},
		{
			name:    "mixed valid and invalid checks",
			input:   ResponsiveAuditInput{Checks: []string{"layout", "fake", "overflow"}},
			wantErr: true,
			errMsg:  "invalid check type",
		},
		{
			name:    "all invalid checks",
			input:   ResponsiveAuditInput{Checks: []string{"foo", "bar"}},
			wantErr: true,
			errMsg:  "invalid check type",
		},
		{
			name:    "negative viewport width",
			input:   ResponsiveAuditInput{Viewports: []ViewportInput{{Name: "bad", Width: -100, Height: 100}}},
			wantErr: true,
			errMsg:  "invalid viewport dimensions",
		},
		{
			name:    "negative viewport height",
			input:   ResponsiveAuditInput{Viewports: []ViewportInput{{Name: "bad", Width: 100, Height: -50}}},
			wantErr: true,
			errMsg:  "invalid viewport dimensions",
		},
		{
			name:    "zero viewport dimensions",
			input:   ResponsiveAuditInput{Viewports: []ViewportInput{{Name: "bad", Width: 0, Height: 0}}},
			wantErr: true,
			errMsg:  "invalid viewport dimensions",
		},
		{
			name:    "negative timeout",
			input:   ResponsiveAuditInput{Timeout: -1000},
			wantErr: true,
			errMsg:  "invalid timeout",
		},
		{
			name:    "valid a11y check",
			input:   ResponsiveAuditInput{Checks: []string{"a11y"}},
			wantErr: false,
		},
		{
			name:    "all valid checks",
			input:   ResponsiveAuditInput{Checks: []string{"layout", "overflow", "a11y"}},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateResponsiveAuditInput(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestBuildAuditOptionsFiltersInvalid(t *testing.T) {
	tests := []struct {
		name    string
		input   ResponsiveAuditInput
		wantVP  int // expected viewport count in output
		wantChk int // expected checks count in output
	}{
		{
			name:    "filters negative viewport dimensions",
			input:   ResponsiveAuditInput{Viewports: []ViewportInput{{Name: "good", Width: 375, Height: 667}, {Name: "bad", Width: -100, Height: 200}}},
			wantVP:  1,
			wantChk: 0,
		},
		{
			name:    "filters zero viewport dimensions",
			input:   ResponsiveAuditInput{Viewports: []ViewportInput{{Name: "bad", Width: 0, Height: 100}}},
			wantVP:  0,
			wantChk: 0,
		},
		{
			name:    "filters invalid check types",
			input:   ResponsiveAuditInput{Checks: []string{"layout", "invalid", "fake"}},
			wantVP:  0,
			wantChk: 1, // only "layout" is valid
		},
		{
			name:    "keeps all valid checks",
			input:   ResponsiveAuditInput{Checks: []string{"layout", "overflow", "a11y"}},
			wantVP:  0,
			wantChk: 3,
		},
		{
			name:    "filters all invalid checks",
			input:   ResponsiveAuditInput{Checks: []string{"foo", "bar"}},
			wantVP:  0,
			wantChk: 0,
		},
		{
			name:    "empty viewport name gets default",
			input:   ResponsiveAuditInput{Viewports: []ViewportInput{{Name: "", Width: 375, Height: 667}}},
			wantVP:  1,
			wantChk: 0,
		},
		{
			name:    "mix of valid and invalid viewports",
			input:   ResponsiveAuditInput{Viewports: []ViewportInput{{Width: 375, Height: 667}, {Width: -1, Height: 100}, {Width: 100, Height: -1}, {Width: 768, Height: 1024}}},
			wantVP:  2,
			wantChk: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildAuditOptions(tt.input)

			if tt.wantVP > 0 {
				vps := result["viewports"].([]map[string]any)
				assert.Len(t, vps, tt.wantVP)
			} else {
				_, hasVP := result["viewports"]
				assert.False(t, hasVP, "viewports should not be present when all invalid")
			}

			if tt.wantChk > 0 {
				checks := result["checks"].([]string)
				assert.Len(t, checks, tt.wantChk)
			} else {
				_, hasChk := result["checks"]
				assert.False(t, hasChk, "checks should not be present when all invalid")
			}
		})
	}
}

func TestValidCheckTypes(t *testing.T) {
	assert.True(t, validCheckTypes["layout"])
	assert.True(t, validCheckTypes["overflow"])
	assert.True(t, validCheckTypes["a11y"])
	assert.False(t, validCheckTypes["invalid"])
	assert.False(t, validCheckTypes[""])
	assert.False(t, validCheckTypes["foo"])
}

func TestResponsiveAuditToolDescription(t *testing.T) {
	// Verify the constant is not empty and contains expected content
	assert.NotEmpty(t, responsiveAuditToolDescription)
	assert.Contains(t, responsiveAuditToolDescription, "responsive")
	assert.Contains(t, responsiveAuditToolDescription, "layout")
	assert.Contains(t, responsiveAuditToolDescription, "overflow")
	assert.Contains(t, responsiveAuditToolDescription, "a11y")
	assert.Contains(t, responsiveAuditToolDescription, "viewport")
}

// TestFindingIDStableFromConverters verifies that the findingID helper used
// by the unified-error converters is stable: same inputs → same 8-char hex ID.
// The responsive_audit tool uses JS-side IDs; Go-side stability is covered
// by unified_error_test.go. This test confirms the shared helper is available
// and consistent from this package.
func TestFindingIDConsistency(t *testing.T) {
	t.Run("identical calls return same id", func(t *testing.T) {
		id1 := findingID("layout", ".header", "collapsed content, element has text but zero height")
		id2 := findingID("layout", ".header", "collapsed content, element has text but zero height")
		assert.Equal(t, id1, id2)
		assert.Len(t, id1, 8)
	})

	t.Run("distinct findings get distinct ids", func(t *testing.T) {
		id1 := findingID("layout", ".header", "collapsed content, element has text but zero height")
		id2 := findingID("overflow", ".sidebar", "forces horizontal scroll +25px")
		assert.NotEqual(t, id1, id2)
	})

	t.Run("id is lowercase hex", func(t *testing.T) {
		id := findingID("a11y", "button.submit", "touch target smaller than 44x44px minimum")
		assert.Regexp(t, "^[0-9a-f]{8}$", id)
	})
}
