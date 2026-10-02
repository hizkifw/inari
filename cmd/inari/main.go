// Command inari connects kon to chat platforms: it runs `kon acp` and serves
// each configured connector's conversations through it.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"

	"github.com/hizkifw/inari/internal/config"
	"github.com/hizkifw/inari/internal/discord"
	"github.com/hizkifw/inari/internal/hub"
	"github.com/hizkifw/inari/internal/store"
)

// version is set at release by -ldflags "-X main.version=vX.Y.Z".
var version string

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "inari:", err)
		os.Exit(1)
	}
}

func run() error {
	path, err := config.DefaultPath()
	if err != nil {
		return err
	}
	flag.StringVar(&path, "config", path, "path to config.json")
	debugLog := flag.Bool("debug", false, "log debug messages")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("inari", buildVersion())
		return nil
	}

	level := slog.LevelInfo
	if *debugLog {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	st, err := store.Open(filepath.Join(cfg.StateDir, "sessions.json"))
	if err != nil {
		return fmt.Errorf("open session store: %w", err)
	}
	h := hub.New(hub.Kon{Command: cfg.Kon.Command, Args: cfg.Kon.Args}, buildVersion(), st, log)
	defer h.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var connectors []interface{ Run(context.Context) error }
	if d := cfg.Connectors.Discord; d != nil {
		c, err := discord.New(d, h, log)
		if err != nil {
			return err
		}
		h.Register("discord", c)
		connectors = append(connectors, c)
	}

	// One connector failing stops them all, so inari never runs half
	// connected without saying so.
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	errs := make(chan error, len(connectors))
	for _, c := range connectors {
		go func() {
			err := c.Run(ctx)
			if err != nil {
				cancel(err)
			}
			errs <- err
		}()
	}
	var all []error
	for range connectors {
		all = append(all, <-errs)
	}
	log.Info("shutting down")
	return errors.Join(all...)
}

// buildVersion is the release's version, or the module version go install
// records, or "dev" for a build from a checkout.
func buildVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}
