package publish

import (
	"fmt"
	"os"
	"path/filepath"
)

// ServeStoreMetaFile is the discovery metadata an `agnt publish serve` run
// drops into its own store dir. It is not a share/feedback record — it exists
// purely so a separate process (`publish list`, `publish feedback --store`)
// can find the store and map it back to the folder it serves.
const ServeStoreMetaFile = "serve.json"

// ServeStoreBase is the parent of every default per-folder serve store, under
// the user cache directory. `publish serve` writes its stores here and
// `publish list` enumerates it to discover them; both resolve the location
// through this one function so the writer and the reader cannot drift apart.
func ServeStoreBase() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolve cache dir: %w", err)
	}
	return filepath.Join(base, "agnt", "publish-serve"), nil
}
