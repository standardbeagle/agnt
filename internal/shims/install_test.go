package shims

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeProjectConfig creates a temp project with a .agnt.kdl.
func writeProjectConfig(t *testing.T, kdl string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".agnt.kdl"), []byte(kdl), 0o644))
	return dir
}

func shimScriptPath(dir, name string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(dir, name+".cmd")
	}
	return filepath.Join(dir, name)
}

func TestEnsureInstallsShimScripts(t *testing.T) {
	t.Parallel()
	project := writeProjectConfig(t, "scripts {}\n")

	binDir, err := Ensure(project)
	require.NoError(t, err)
	assert.Equal(t, BinDir(project), binDir)

	data, err := os.ReadFile(shimScriptPath(binDir, "npm"))
	require.NoError(t, err)
	content := string(data)
	assert.Contains(t, content, shimMarker)
	assert.Contains(t, content, "shim exec")
	assert.Contains(t, content, "npm")

	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(binDir, "npm"))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o755), info.Mode().Perm())
	}
}

func TestEnsureIdempotent(t *testing.T) {
	t.Parallel()
	project := writeProjectConfig(t, "scripts {}\n")

	binDir, err := Ensure(project)
	require.NoError(t, err)
	npmPath := shimScriptPath(binDir, "npm")
	first, err := os.ReadFile(npmPath)
	require.NoError(t, err)

	_, err = Ensure(project)
	require.NoError(t, err)
	second, err := os.ReadFile(npmPath)
	require.NoError(t, err)
	assert.Equal(t, string(first), string(second))
}

func TestEnsureSkipsWithoutConfig(t *testing.T) {
	t.Parallel()
	project := t.TempDir() // no .agnt.kdl
	binDir, err := Ensure(project)
	require.NoError(t, err)
	assert.Equal(t, "", binDir)
	_, statErr := os.Stat(BinDir(project))
	assert.True(t, os.IsNotExist(statErr))
}

func TestEnsureSkipsWhenDisabled(t *testing.T) {
	t.Parallel()
	project := writeProjectConfig(t, "shims {\n    enabled false\n}\n")
	binDir, err := Ensure(project)
	require.NoError(t, err)
	assert.Equal(t, "", binDir)
}

func TestEnsureLeavesUserFilesAlone(t *testing.T) {
	t.Parallel()
	project := writeProjectConfig(t, "scripts {}\n")
	binDir := BinDir(project)
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	userFile := shimScriptPath(binDir, "npm")
	require.NoError(t, os.WriteFile(userFile, []byte("#!/bin/sh\n# my custom npm\n"), 0o755))

	_, err := Ensure(project)
	require.NoError(t, err)
	data, err := os.ReadFile(userFile)
	require.NoError(t, err)
	assert.Contains(t, string(data), "my custom npm")
}

func TestRemoveKeepsUserFiles(t *testing.T) {
	t.Parallel()
	project := writeProjectConfig(t, "scripts {}\n")
	binDir, err := Ensure(project)
	require.NoError(t, err)

	userFile := filepath.Join(binDir, "notes.txt")
	require.NoError(t, os.WriteFile(userFile, []byte("mine"), 0o644))

	assert.False(t, Remove(project), "dir with user files must survive")
	_, err = os.Stat(userFile)
	assert.NoError(t, err)
	// Shim scripts are gone.
	_, err = os.Stat(shimScriptPath(binDir, "npm"))
	assert.True(t, os.IsNotExist(err))
}

func TestRemoveDeletesCleanDir(t *testing.T) {
	t.Parallel()
	project := writeProjectConfig(t, "scripts {}\n")
	binDir, err := Ensure(project)
	require.NoError(t, err)

	assert.True(t, Remove(project))
	_, err = os.Stat(binDir)
	assert.True(t, os.IsNotExist(err))
}

func TestRemoveMissingDirIsNoop(t *testing.T) {
	t.Parallel()
	assert.True(t, Remove(t.TempDir()))
}

func TestPrependPATH(t *testing.T) {
	t.Parallel()
	env := PrependPATH([]string{"FOO=1", "PATH=/usr/bin:/bin"}, "/proj/.agnt/bin")
	sep := string(os.PathListSeparator)
	var got string
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			got = strings.TrimPrefix(kv, "PATH=")
		}
	}
	assert.Equal(t, "/proj/.agnt/bin"+sep+"/usr/bin"+sep+"/bin", got)

	// No PATH entry → appended.
	env = PrependPATH([]string{"FOO=1"}, "/x")
	assert.Contains(t, env, "PATH=/x")

	// Empty dir → unchanged.
	env = PrependPATH([]string{"PATH=/usr/bin"}, "")
	assert.Equal(t, []string{"PATH=/usr/bin"}, env)
}

func TestResolveRealBinarySkipsShimDirs(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	shimDir := filepath.Join(root, ".agnt", "bin")
	realDir := filepath.Join(root, "real")
	require.NoError(t, os.MkdirAll(shimDir, 0o755))
	require.NoError(t, os.MkdirAll(realDir, 0o755))

	name := "frobnicate"
	shimPath := filepath.Join(shimDir, name)
	realPath := filepath.Join(realDir, name)
	require.NoError(t, os.WriteFile(shimPath, []byte("#!/bin/sh\n"), 0o755))
	require.NoError(t, os.WriteFile(realPath, []byte("#!/bin/sh\n"), 0o755))

	env := []string{"PATH=" + shimDir + string(os.PathListSeparator) + realDir}
	assert.Equal(t, realPath, ResolveRealBinary(name, env))

	// Shim dir only → not found (never exec ourselves).
	env = []string{"PATH=" + shimDir}
	assert.Equal(t, "", ResolveRealBinary(name, env))
}
