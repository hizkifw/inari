package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/hizkifw/inari/internal/buildinfo"
	"github.com/hizkifw/inari/internal/daemon"
	"github.com/hizkifw/inari/internal/selfupdate"
)

// runUpgrade installs the latest release over this executable, or with
// --check only reports whether there is one.
func runUpgrade(args []string) error {
	fs := flag.NewFlagSet("inari upgrade", flag.ContinueOnError)
	check := fs.Bool("check", false, "report whether a newer release exists without installing it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("usage: inari upgrade [--check]")
	}
	current, err := selfupdate.ParseVersion(buildinfo.Version())
	if err != nil {
		return fmt.Errorf("cannot upgrade a development build (version %q); "+
			"reinstall with go install or the release installer", buildinfo.Version())
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	updater := selfupdate.New()
	latest, err := updater.Latest(ctx)
	if err != nil {
		return err
	}
	if latest.Compare(current) <= 0 {
		fmt.Printf("inari %s is up to date (latest release is %s)\n", current, latest)
		return nil
	}
	if *check {
		fmt.Printf("inari %s is available (current %s); run \"inari upgrade\" to install it\n", latest, current)
		return nil
	}

	// Resolve symlinks so an installer's symlink keeps pointing at the
	// upgraded file instead of being replaced by a regular file.
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate inari executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	fmt.Fprintf(os.Stderr, "downloading inari %s\n", latest)
	if err := updater.Install(ctx, latest, exe); err != nil {
		return fmt.Errorf("upgrade to %s: %w", latest, err)
	}
	fmt.Printf("upgraded inari %s → %s\n", current, latest)
	restartService()
	return nil
}

// restartService moves an inari running as a service onto the new file. A
// running inari keeps executing the old one until it restarts. When kon runs
// the upgrade from inside that service, the restart stops this process too,
// so everything worth saying is printed before it.
func restartService() {
	m, err := daemon.Detect()
	if err != nil {
		fmt.Println("restart inari to run it")
		return
	}
	fmt.Printf("restarting the %s service, if it is running\n", m.Name())
	restarted, err := m.Restart()
	switch {
	case err != nil:
		fmt.Fprintf(os.Stderr, "inari: restart the service: %v\n", err)
	case !restarted:
		fmt.Println("the service is not running; restart inari to run it")
	}
}
