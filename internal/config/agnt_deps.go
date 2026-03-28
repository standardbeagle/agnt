package config

import (
	"fmt"
	"time"

	"github.com/sblinch/kdl-go/document"
)

// DefaultDependencyTimeout is the default timeout for script dependencies.
// 120s accommodates slow-starting processes like dotnet run (NuGet restore + compile).
const DefaultDependencyTimeout = 120 * time.Second

// ScriptDependency represents a dependency on another script.
type ScriptDependency struct {
	// Name is the name of the script this depends on.
	Name string
	// Timeout is how long to wait for the dependency to become ready.
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
		timeout := DefaultDependencyTimeout
		if node.Properties.Exist() {
			if tv, ok := node.Properties.Get("timeout"); ok {
				if tval, ok := toSeconds(tv.Value); ok {
					timeout = tval
				}
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
		dep := ScriptDependency{Name: name, Timeout: DefaultDependencyTimeout}
		if child.Properties.Exist() {
			if tv, ok := child.Properties.Get("timeout"); ok {
				if tval, ok := toSeconds(tv.Value); ok {
					dep.Timeout = tval
				}
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
