package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/hizkifw/inari/internal/config"
	"github.com/hizkifw/inari/internal/daemon"
)

const daemonUsage = `usage: inari daemon <command>

Run inari in the background under this machine's service manager (a systemd
user service on Linux), which starts it at login and again when it fails.
"inari upgrade" restarts it onto the new release.

  inari daemon install [-config PATH] [-debug]
  inari daemon uninstall

install checks the config, then installs and starts the service, replacing
an earlier install. The service keeps this shell's PATH and XDG
directories, so run it where inari runs.`

// envKept are the variables a service needs from the installing shell: PATH
// finds kon, and the XDG directories locate inari's and kon's state.
var envKept = []string{"PATH", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"}

func runDaemon(args []string, stdout io.Writer) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprintln(stdout, daemonUsage)
		return nil
	}
	switch args[0] {
	case "install":
		return daemonInstall(args[1:], stdout)
	case "uninstall":
		if len(args) != 1 {
			return errors.New("usage: inari daemon uninstall")
		}
		m, err := daemon.Detect()
		if err != nil {
			return err
		}
		if err := m.Uninstall(); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "stopped and removed the %s service\n", m.Name())
		return nil
	}
	return fmt.Errorf("unknown daemon command %q; run inari daemon --help", args[0])
}

func daemonInstall(args []string, stdout io.Writer) error {
	path, err := config.DefaultPath()
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("inari daemon install", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&path, "config", path, "")
	debug := fs.Bool("debug", false, "")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w; run inari daemon --help", err)
	}
	if fs.NArg() > 0 {
		return errors.New("usage: inari daemon install [-config PATH] [-debug]")
	}
	m, err := daemon.Detect()
	if err != nil {
		return err
	}

	// A service that cannot start would only fail in a log, so the config
	// is checked here, with the shell's environment, first.
	if path, err = filepath.Abs(path); err != nil {
		return err
	}
	if _, err := config.Load(path); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate inari executable: %w", err)
	}
	spec := daemon.Spec{Executable: exe, Args: []string{"-config", path}, Env: map[string]string{}}
	if *debug {
		spec.Args = append(spec.Args, "-debug")
	}
	for _, k := range envKept {
		if v, ok := os.LookupEnv(k); ok {
			spec.Env[k] = v
		}
	}

	notes, err := m.Install(spec)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "inari is running as a %s service\n", m.Name())
	for _, n := range notes {
		fmt.Fprintln(stdout, "  "+n)
	}
	return nil
}
