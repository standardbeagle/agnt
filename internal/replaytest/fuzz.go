package replaytest

import (
	"encoding/json"
	"sort"
)

type FuzzResult struct {
	Status int
	Body   string
}

type FuzzPreset struct {
	Name  string
	Apply func(status int, body string) FuzzResult
}

var presets = map[string]FuzzPreset{
	"empty_array": {Name: "empty_array", Apply: func(s int, b string) FuzzResult {
		return FuzzResult{Status: s, Body: replaceArraysWithEmpty(b)}
	}},
	"http_error": {Name: "http_error", Apply: func(s int, b string) FuzzResult {
		return FuzzResult{Status: 500, Body: `{"error":"injected"}`}
	}},
	"truncated_json": {Name: "truncated_json", Apply: func(s int, b string) FuzzResult {
		if len(b) > 1 {
			b = b[:len(b)/2]
		}
		return FuzzResult{Status: s, Body: b}
	}},
	"null_fields": {Name: "null_fields", Apply: func(s int, b string) FuzzResult {
		return FuzzResult{Status: s, Body: nullifyLeafValues(b)}
	}},
	"reordered": {Name: "reordered", Apply: func(s int, b string) FuzzResult {
		return FuzzResult{Status: s, Body: reverseTopArray(b)}
	}},
	"type_flip": {Name: "type_flip", Apply: func(s int, b string) FuzzResult {
		return FuzzResult{Status: s, Body: flipScalarTypes(b)}
	}},
}

func Preset(name string) (FuzzPreset, bool) { p, ok := presets[name]; return p, ok }

func PresetNames() []string {
	names := make([]string, 0, len(presets))
	for n := range presets {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func replaceArraysWithEmpty(b string) string {
	var v any
	if json.Unmarshal([]byte(b), &v) != nil {
		return b
	}
	out, _ := json.Marshal(emptyArrays(v))
	return string(out)
}

func emptyArrays(v any) any {
	switch t := v.(type) {
	case []any:
		return []any{}
	case map[string]any:
		for k, vv := range t {
			t[k] = emptyArrays(vv)
		}
		return t
	default:
		return v
	}
}

func nullifyLeafValues(b string) string {
	var v any
	if json.Unmarshal([]byte(b), &v) != nil {
		return b
	}
	out, _ := json.Marshal(nullifyLeaves(v))
	return string(out)
}

func nullifyLeaves(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k := range t {
			t[k] = nil
		}
		return t
	case []any:
		for i := range t {
			t[i] = nil
		}
		return t
	default:
		return nil
	}
}

func reverseTopArray(b string) string {
	var arr []any
	if json.Unmarshal([]byte(b), &arr) != nil {
		return b
	}
	for i, j := 0, len(arr)-1; i < j; i, j = i+1, j-1 {
		arr[i], arr[j] = arr[j], arr[i]
	}
	out, _ := json.Marshal(arr)
	return string(out)
}

func flipScalarTypes(b string) string {
	var m map[string]any
	if json.Unmarshal([]byte(b), &m) != nil {
		return b
	}
	for k, v := range m {
		switch tv := v.(type) {
		case float64:
			m[k] = "flipped"
		case string:
			m[k] = len(tv)
		}
	}
	out, _ := json.Marshal(m)
	return string(out)
}
