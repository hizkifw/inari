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
	"strings"
	"syscall"
	// Schedules name IANA time zones, which a Windows machine has no database
	// of.
	_ "time/tzdata"

	"github.com/hizkifw/inari/internal/buildinfo"
	"github.com/hizkifw/inari/internal/config"
	"github.com/hizkifw/inari/internal/cron"
	"github.com/hizkifw/inari/internal/discord"
	"github.com/hizkifw/inari/internal/hub"
	"github.com/hizkifw/inari/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "inari:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "upgrade":
			return runUpgrade(os.Args[2:])
		case "cron":
			return runCron(os.Args[2:], os.Stdin, os.Stdout)
		}
	}
	flag.Usage = func() {
		fmt.Fprintln(flag.CommandLine.Output(), "usage: inari [flags]\n       inari upgrade [--check]\n       inari cron <command>\n\nflags:")
		flag.PrintDefaults()
	}
	path, err := config.DefaultPath()
	if err != nil {
		return err
	}
	flag.StringVar(&path, "config", path, "path to config.json")
	debugLog := flag.Bool("debug", false, "log debug messages")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	// An argument this build does not know, such as a subcommand from a
	// newer release, must not start the bot.
	if flag.NArg() > 0 {
		flag.Usage()
		return fmt.Errorf("unknown command %q", flag.Arg(0))
	}
	if *showVersion {
		// selfupdate checks a downloaded release by this exact line.
		fmt.Println("inari", buildinfo.Version())
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
	// kon inherits this, so an `inari cron` it runs reads this config.
	if abs, err := filepath.Abs(path); err == nil {
		os.Setenv(config.ConfigEnv, abs)
	}
	h := hub.New(hub.Kon{Command: cfg.Kon.Command, Args: cfg.Kon.Args}, buildinfo.Version(), st, log)
	defer h.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var connectors []interface{ Run(context.Context) error }
	routes := map[string]func(channel string) (hub.Route, bool){}
	if d := cfg.Connectors.Discord; d != nil {
		c, err := discord.New(d, h, log)
		if err != nil {
			return err
		}
		h.Register("discord", c)
		connectors = append(connectors, c)
		routes["discord"] = c.Route
	}
	if err := startCron(ctx, cfg, h, routes, log); err != nil {
		return err
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

// startCron runs the jobs kon schedules, when the config names a home, and
// tells chat sessions how to schedule them.
func startCron(ctx context.Context, cfg *config.Config, h *hub.Hub, routes map[string]func(string) (hub.Route, bool), log *slog.Logger) error {
	if cfg.Cron.Home == "" {
		return nil
	}
	connector, channel, _ := strings.Cut(cfg.Cron.Home, ":")
	routeOf, ok := routes[connector]
	if !ok {
		return fmt.Errorf("cron: home %q names no running connector", cfg.Cron.Home)
	}
	route, ok := routeOf(channel)
	if !ok {
		return fmt.Errorf("cron: home %q is not a channel inari serves", cfg.Cron.Home)
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cron: locate inari: %w", err)
	}
	h.SetInstructions(cron.Usage(exe, cfg.Cron.Location))
	runner := cron.NewRunner(h, cron.Target{Conv: cfg.Cron.Home, Route: route}, cfg.Cron.Limit, log)
	dir := cron.Dir(filepath.Join(cfg.StateDir, "cron"))
	go cron.NewScheduler(dir, cfg.Cron.Location, runner.Run, log).Run(ctx)
	log.Info("cron on", "home", cfg.Cron.Home, "jobs", string(dir), "timezone", cfg.Cron.Location)
	return nil
}
