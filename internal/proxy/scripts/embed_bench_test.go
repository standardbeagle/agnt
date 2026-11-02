package scripts

import "testing"

// BenchmarkBuildAllRoleBundles is the cold cost the first proxied page pays:
// every role's bundle assembled from the embedded modules. Floor: copying
// the output bytes once, a few ms for ~5 MB.
func BenchmarkBuildAllRoleBundles(b *testing.B) {
	total := 0
	for _, role := range []Role{RoleFull, RoleChrome, RoleContent, RolePublic} {
		total += len(buildCombinedScript(role))
	}
	b.SetBytes(int64(total))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, role := range []Role{RoleFull, RoleChrome, RoleContent, RolePublic} {
			_ = buildCombinedScript(role)
		}
	}
}

// BenchmarkWrapAllModules is the once-per-process half of the cold cost:
// stripping every module's IIFE before the bundles share the result.
func BenchmarkWrapAllModules(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		for _, m := range moduleOrder {
			_ = wrapModule(moduleScript[m.name])
		}
	}
}
