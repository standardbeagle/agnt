package config

import (
	"fmt"
	"time"

	"github.com/sblinch/kdl-go/document"
)

// DefaultDependencyTimeout is the legacy default timeout for script
// dependencies. The current parser defaults to 0 (wait indefinitely until
// the parent context is cancelled or an explicit per-dep timeout is set in
// .agnt.kdl), so this constant is retained only for backward compatibility
// with internal/autostart and existing tests.
const DefaultDependencyTimeout = 120 * time.Second

// ScriptDependency represents a dependency on another script.
type ScriptDependency struct {
	// Name is the name of the script this depends on.
	Name string
	// Timeout is how long to wait for the dependency to become ready.
	// Zero means wait indefinitely (until the autostart context is cancelled
	// or the dependency's ready signal arrives). A positive value enforces
	// a hard upper bound on the wait — only set when the user explicitly
	// configures `timeout=N` in .agnt.kdl.
	Timeout time.Duration
}

// DependsOnList is a list of script dependencies with custom KDL unmarshaling.
// Supports two KDL formats:
//
// Arguments format (all deps get default or node-level timeout):
//
//	depends-on "api" "redis"
//	depends-on "api" timeout=45
//
// Child node format (per-dependency timeouts):
//
//	depends-on {
//	    api timeout=30
//	    redis timeout=60
//	}
type DependsOnList []ScriptDependency

// UnmarshalKDL implements the kdl.Unmarshaler interface for custom parsing.
func (d *DependsOnList) UnmarshalKDL(node *document.Node) error {
	if len(node.Children) == 0 && len(node.Arguments) == 0 {
		return fmt.Errorf("depends-on requires at least one dependency name")
	}

	// Arguments-only format: depends-on "api" "redis" timeout=45
	if len(node.Children) == 0 {
		// Default to 0 (wait indefinitely). Per .claude/rules/daemon-lifecycle.md
		// a Starting process must never be abandoned by a hardcoded timeout —
		// only an explicit user-set `timeout=N` should bound the wait.
		var timeout time.Duration
		if node.Properties.Exist() {
			if tv, ok := node.Properties.Get("timeout"); ok {
				tval, ok := toSeconds(tv.Value)
				if !ok {
					// The user declared timeout=N in .agnt.kdl; a non-numeric
					// value must fail loudly rather than silently fall back to
					// "wait indefinitely" (Silent Failure Prohibition).
					return fmt.Errorf("depends-on timeout must be a number of seconds, got %T (%v)", tv.Value, tv.Value)
				}
				timeout = tval
			}
		}
		for _, arg := range node.Arguments {
			name, ok := arg.Value.(string)
			if !ok {
				return fmt.Errorf("depends-on argument must be a string, got %T", arg.Value)
			}
			*d = append(*d, ScriptDependency{Name: name, Timeout: timeout})
		}
		return nil
	}

	// Mixed args and children is an error
	if len(node.Arguments) > 0 {
		return fmt.Errorf("depends-on cannot have both arguments and child nodes")
	}

	// Child node format: depends-on { api timeout=30; redis timeout=60 }
	for _, child := range node.Children {
		name := child.Name.ValueString()
		// Default to 0 (wait indefinitely). See note in the args branch above.
		dep := ScriptDependency{Name: name, Timeout: 0}
		if child.Properties.Exist() {
			if tv, ok := child.Properties.Get("timeout"); ok {
				tval, ok := toSeconds(tv.Value)
				if !ok {
					// Same as the args branch: a malformed per-dependency
					// timeout the user declared must surface, not be dropped.
					return fmt.Errorf("depends-on %q timeout must be a number of seconds, got %T (%v)", name, tv.Value, tv.Value)
				}
				dep.Timeout = tval
			}
		}
		*d = append(*d, dep)
	}
	return nil
}

// toSeconds converts a numeric value to a time.Duration in seconds.
func toSeconds(v interface{}) (time.Duration, bool) {
	switch val := v.(type) {
	case int:
		return time.Duration(val) * time.Second, true
	case int64:
		return time.Duration(val) * time.Second, true
	case float64:
		return time.Duration(val) * time.Second, true
	}
	return 0, false
}

// TopologicalSort sorts scripts into execution layers based on their dependencies.
// Returns layers where each layer's scripts can run in parallel, and all dependencies
// from prior layers are satisfied. Returns an error if a circular dependency is detected.
func TopologicalSort(scripts map[string]*ScriptConfig) ([][]string, error) {
	// Build adjacency and in-degree maps
	inDegree := make(map[string]int, len(scripts))
	dependents := make(map[string][]string, len(scripts)) // dep -> scripts that depend on it

	for name := range scripts {
		inDegree[name] = 0
	}

	for name, script := range scripts {
		for _, dep := range script.DependsOn {
			if _, exists := scripts[dep.Name]; !exists {
				return nil, fmt.Errorf("script %q depends on unknown script %q", name, dep.Name)
			}
			inDegree[name]++
			dependents[dep.Name] = append(dependents[dep.Name], name)
		}
	}

	// Kahn's algorithm: process nodes with zero in-degree in layers
	var layers [][]string
	processed := 0

	for processed < len(scripts) {
		// Collect all nodes with zero in-degree
		var layer []string
		for name, deg := range inDegree {
			if deg == 0 {
				layer = append(layer, name)
			}
		}

		if len(layer) == 0 {
			// Find cycle participants for error message
			var cycleNodes []string
			for name, deg := range inDegree {
				if deg > 0 {
					cycleNodes = append(cycleNodes, name)
				}
			}
			return nil, fmt.Errorf("circular dependency detected among scripts: %v", cycleNodes)
		}

		// Remove processed nodes and decrement dependents
		for _, name := range layer {
			delete(inDegree, name)
			for _, dependent := range dependents[name] {
				if _, exists := inDegree[dependent]; exists {
					inDegree[dependent]--
				}
			}
		}

		layers = append(layers, layer)
		processed += len(layer)
	}

	return layers, nil
}

// ValidateDependencies checks dependency configuration for errors and warnings.
// Returns an error for fatal issues (unknown deps, cycles) and a list of
// warning messages for non-fatal issues (deps on non-autostart scripts).
func ValidateDependencies(scripts map[string]*ScriptConfig) (warnings []string, err error) {
	// Check for unknown dependency names
	for name, script := range scripts {
		for _, dep := range script.DependsOn {
			if _, exists := scripts[dep.Name]; !exists {
				return nil, fmt.Errorf("script %q depends on unknown script %q", name, dep.Name)
			}
		}
	}

	// Check for circular dependencies via topological sort
	if _, err := TopologicalSort(scripts); err != nil {
		return nil, err
	}

	// Warn about dependencies on non-autostart scripts
	for name, script := range scripts {
		for _, dep := range script.DependsOn {
			depScript := scripts[dep.Name]
			if !depScript.Autostart {
				warnings = append(warnings, fmt.Sprintf(
					"script %q depends on %q which is not set to autostart",
					name, dep.Name,
				))
			}
		}
	}

	return warnings, nil
}
