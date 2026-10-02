// Package daemon runs inari as a background service under the platform's
// service manager. Each service manager implements Manager, and Detect picks
// the one this machine has, so callers never name a particular one.
package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Service is the name inari is installed under.
const Service = "inari"

var (
	// ErrUnsupported is returned by Detect on a machine with no service
	// manager inari knows.
	ErrUnsupported = errors.New("no supported service manager on this machine")
	// ErrNotInstalled is returned by Uninstall when there is no service.
	ErrNotInstalled = errors.New("inari is not installed as a service")
)

// Spec is the service to install: what it runs and in which environment.
type Spec struct {
	// Executable is the absolute path of the inari to run.
	Executable string
	Args       []string
	// Env is set in the service's environment. A service manager starts
	// services with an environment of its own rather than the shell's, so
	// the caller passes what inari needs from the shell, such as the PATH
	// that finds kon.
	Env map[string]string
}

// Manager installs inari as a service with one service manager.
type Manager interface {
	// Name names the service manager, such as "systemd".
	Name() string
	// Install installs the service, enables it to start on its own, and
	// starts it, replacing an earlier install. The notes are what the user
	// should know about the result, such as where its logs go.
	Install(Spec) (notes []string, err error)
	// Uninstall stops the service and removes it.
	Uninstall() error
	// Restart restarts the service when it is running and reports whether it
	// was. It does not wait for the restart, so inari's own kon may call it
	// through `inari upgrade`: the service manager finishes the restart after
	// stopping the caller along with the service.
	Restart() (bool, error)
}

// Detect returns the service manager of this machine.
func Detect() (Manager, error) {
	if runtime.GOOS == "linux" && systemdBooted() {
		return newSystemd()
	}
	return nil, fmt.Errorf("%w (%s)", ErrUnsupported, runtime.GOOS)
}

// writeFile replaces path atomically, so a failed write never leaves a
// half-written service definition behind.
func writeFile(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	err = errors.Join(err, f.Close())
	if err == nil {
		err = os.Chmod(f.Name(), perm)
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}
