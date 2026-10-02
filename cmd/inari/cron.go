package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/hizkifw/inari/internal/config"
	"github.com/hizkifw/inari/internal/cron"
)

const cronUsage = `usage: inari cron <command>

Schedule a prompt to run in a new, empty kon session. When a run finishes,
its final message is delivered to the home channel's agent as a notice.

  inari cron add --name NAME (--schedule SPEC | --at TIME | --in DURATION) [--cwd DIR] [--replace] PROMPT
  inari cron list
  inari cron remove NAME

SPEC is a cron expression ("0 9 * * 1-5": minute hour day-of-month month
day-of-week), @hourly, @daily, @weekly, @monthly, or "@every 30m".
TIME is "2006-01-02 15:04", "15:04" (the next one), or RFC 3339.
DURATION is a Go duration such as 90m or 2h30m.
PROMPT is the rest of the arguments, or "-" to read it from stdin.
The job's session works in DIR, by default the current directory.`

// runCron is `inari cron`. It works on the job directory alone, so it needs
// no running inari: the scheduler notices changes within seconds.
func runCron(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprintln(stdout, cronUsage)
		return nil
	}
	cfg, err := cronConfig()
	if err != nil {
		return err
	}
	dir := cron.Dir(filepath.Join(cfg.StateDir, "cron"))
	loc := cfg.Cron.Location
	switch args[0] {
	case "add":
		return cronAdd(dir, loc, args[1:], stdin, stdout)
	case "list":
		return cronList(dir, loc, stdout)
	case "remove", "rm":
		if len(args) != 2 {
			return errors.New("usage: inari cron remove NAME")
		}
		if err := dir.Remove(args[1]); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "removed %s\n", args[1])
		return nil
	}
	return fmt.Errorf("unknown cron command %q; run inari cron --help", args[0])
}

// cronConfig reads the config of the inari that started this kon, when kon
// runs the command, and the default one otherwise.
func cronConfig() (*config.Config, error) {
	path := os.Getenv(config.ConfigEnv)
	if path == "" {
		var err error
		if path, err = config.DefaultPath(); err != nil {
			return nil, err
		}
	}
	cfg, err := config.LoadCron(path)
	if err != nil {
		return nil, err
	}
	if cfg.Cron.Home == "" {
		return nil, fmt.Errorf("cron is off: set cron.home in %s", path)
	}
	return cfg, nil
}

func cronAdd(dir cron.Dir, loc *time.Location, args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("inari cron add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	name := fs.String("name", "", "")
	schedule := fs.String("schedule", "", "")
	atText := fs.String("at", "", "")
	in := fs.Duration("in", 0, "")
	cwd := fs.String("cwd", "", "")
	replace := fs.Bool("replace", false, "")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w; run inari cron --help", err)
	}
	now := time.Now().In(loc)
	j := cron.Job{Name: *name, Schedule: *schedule, CWD: *cwd, Created: now}

	set := 0
	for _, given := range []bool{*schedule != "", *atText != "", *in != 0} {
		if given {
			set++
		}
	}
	if set != 1 {
		return errors.New("give exactly one of --schedule, --at, and --in")
	}
	switch {
	case *atText != "":
		t, err := parseAt(*atText, now)
		if err != nil {
			return err
		}
		j.At = t
	case *in != 0:
		if *in < 0 {
			return errors.New("--in must be positive")
		}
		j.At = now.Add(*in).Truncate(time.Second)
	}
	if !j.At.IsZero() && !j.At.After(now) {
		return fmt.Errorf("%s has already passed", j.At.Format(timeFormat))
	}

	prompt := strings.Join(fs.Args(), " ")
	if prompt == "-" {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return err
		}
		prompt = string(data)
	}
	j.Prompt = strings.TrimSpace(prompt)
	if j.CWD == "" {
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		j.CWD = wd
	}
	if abs, err := filepath.Abs(j.CWD); err == nil {
		j.CWD = abs
	}
	if info, err := os.Stat(j.CWD); err != nil || !info.IsDir() {
		return fmt.Errorf("%s is not a directory", j.CWD)
	}

	if err := dir.Add(j, *replace); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "added %s: next run %s\n", j.Name, nextRun(j, now))
	return nil
}

func cronList(dir cron.Dir, loc *time.Location, stdout io.Writer) error {
	jobs, errs := dir.List()
	if len(jobs) == 0 && len(errs) == 0 {
		fmt.Fprintln(stdout, "No jobs.")
		return nil
	}
	now := time.Now().In(loc)
	w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSCHEDULE\tNEXT RUN\tDIRECTORY")
	for _, j := range jobs {
		when, _ := j.When()
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", j.Name, when, nextRun(j, now), j.CWD)
	}
	w.Flush()
	for _, j := range jobs {
		fmt.Fprintf(stdout, "\n%s: %s\n", j.Name, j.Prompt)
	}
	for _, err := range errs {
		fmt.Fprintf(stdout, "\nskipped: %v\n", err)
	}
	return nil
}

const timeFormat = "Mon 2006-01-02 15:04 MST"

func nextRun(j cron.Job, now time.Time) string {
	when, err := j.When()
	if err != nil {
		return "never"
	}
	next := when.Next(now)
	if when.Once() && next.IsZero() {
		// Due already; the scheduler runs it on its next poll.
		return "now"
	}
	if next.IsZero() {
		return "never"
	}
	return next.In(now.Location()).Format(timeFormat)
}

// parseAt reads a moment in now's location: a date and time, a time of day
// (the next one), or RFC 3339.
func parseAt(s string, now time.Time) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	for _, layout := range []string{"2006-01-02 15:04", "2006-01-02T15:04", "2006-01-02 15:04:05"} {
		if t, err := time.ParseInLocation(layout, s, now.Location()); err == nil {
			return t, nil
		}
	}
	if t, err := time.ParseInLocation("15:04", s, now.Location()); err == nil {
		at := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location())
		if !at.After(now) {
			at = at.AddDate(0, 0, 1)
		}
		return at, nil
	}
	return time.Time{}, fmt.Errorf("--at %q is not a time; use \"2006-01-02 15:04\", \"15:04\", or RFC 3339", s)
}
