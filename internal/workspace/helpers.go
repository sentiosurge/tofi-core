package workspace

import (
	"os"
)

// readDirSafe reads a directory, returning empty slice if it doesn't exist.
func readDirSafe(dir string) ([]os.DirEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return entries, nil
}
