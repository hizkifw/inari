//go:build !windows

package selfupdate

import "os"

// replace renames over the target. The directory entry changes atomically, and
// a inari that is already running keeps executing the old file.
func replace(target, candidate string) error {
	return os.Rename(candidate, target)
}

func removeStale(string) {}
