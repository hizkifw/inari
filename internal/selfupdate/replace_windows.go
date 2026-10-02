//go:build windows

package selfupdate

import (
	"errors"
	"os"
)

// replace moves the target aside before installing the candidate. Windows
// refuses to overwrite a running executable but allows renaming one.
func replace(target, candidate string) error {
	old := target + ".old"
	if err := os.Rename(target, old); err != nil {
		return err
	}
	if err := os.Rename(candidate, target); err != nil {
		return errors.Join(err, os.Rename(old, target))
	}
	return nil
}

// removeStale deletes the binary a previous upgrade moved aside. That upgrade
// could not delete it because the file was still running; by now it has exited.
func removeStale(target string) {
	_ = os.Remove(target + ".old")
}
