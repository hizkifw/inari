package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// cronEnv points `inari cron` at a config of its own, as inari does for kon.
func cronEnv(t *testing.T, cron string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	body := `{"state_dir": "` + filepath.ToSlash(dir) + `", "cron": ` + cron + `}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("INARI_CONFIG", path)
	return dir
}

func cronCmd(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := runCron(args, strings.NewReader(stdin), &out)
	return out.String(), err
}

func TestCronAddListRemove(t *testing.T) {
	state := cronEnv(t, `{"home": "discord:1", "timezone": "UTC"}`)
	work := t.TempDir()
	out, err := cronCmd(t, "", "add", "--name", "standup", "--schedule", "0 9 * * 1-5", "--cwd", work, "Summarize", "yesterday.")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "added standup: next run ") || !strings.Contains(out, "09:00 UTC") {
		t.Fatalf("add said %q", out)
	}
	if _, err := os.Stat(filepath.Join(state, "cron", "standup.json")); err != nil {
		t.Fatalf("job file: %v", err)
	}
	if _, err := cronCmd(t, "Check the deploy.\n", "add", "--name", "deploy", "--in", "2h", "--cwd", work, "-"); err != nil {
		t.Fatal(err)
	}
	out, err = cronCmd(t, "", "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"standup", "0 9 * * 1-5", "deploy", "at ", "standup: Summarize yesterday.", "deploy: Check the deploy."} {
		if !strings.Contains(out, want) {
			t.Fatalf("list lacks %q:\n%s", want, out)
		}
	}
	if _, err := cronCmd(t, "", "remove", "standup"); err != nil {
		t.Fatal(err)
	}
	if out, _ := cronCmd(t, "", "list"); strings.Contains(out, "standup") {
		t.Fatalf("removed job listed:\n%s", out)
	}
}

func TestCronAddDefaultsToTheCurrentDirectory(t *testing.T) {
	cronEnv(t, `{"home": "discord:1"}`)
	work := t.TempDir()
	t.Chdir(work)
	if _, err := cronCmd(t, "", "add", "--name", "here", "--schedule", "@daily", "go"); err != nil {
		t.Fatal(err)
	}
	out, _ := cronCmd(t, "", "list")
	if !strings.Contains(out, work) {
		t.Fatalf("list does not show %s:\n%s", work, out)
	}
}

func TestCronAddRejects(t *testing.T) {
	cronEnv(t, `{"home": "discord:1"}`)
	work := t.TempDir()
	for name, args := range map[string][]string{
		"no schedule":    {"add", "--name", "x", "--cwd", work, "p"},
		"two schedules":  {"add", "--name", "x", "--schedule", "@daily", "--in", "1h", "--cwd", work, "p"},
		"past time":      {"add", "--name", "x", "--at", "2000-01-01 00:00", "--cwd", work, "p"},
		"bad time":       {"add", "--name", "x", "--at", "tomorrow-ish", "--cwd", work, "p"},
		"no prompt":      {"add", "--name", "x", "--schedule", "@daily", "--cwd", work},
		"bad schedule":   {"add", "--name", "x", "--schedule", "sometimes", "--cwd", work, "p"},
		"missing dir":    {"add", "--name", "x", "--schedule", "@daily", "--cwd", filepath.Join(work, "nope"), "p"},
		"unknown option": {"add", "--name", "x", "--every", "1h", "--cwd", work, "p"},
	} {
		if _, err := cronCmd(t, "", args...); err == nil {
			t.Errorf("%s: added", name)
		}
	}
}

func TestCronIsOffWithoutAHome(t *testing.T) {
	cronEnv(t, `{}`)
	if _, err := cronCmd(t, "", "list"); err == nil || !strings.Contains(err.Error(), "cron.home") {
		t.Fatalf("list without a home: %v", err)
	}
}

func TestParseAt(t *testing.T) {
	now := time.Date(2026, 10, 2, 10, 30, 0, 0, time.UTC)
	for in, want := range map[string]time.Time{
		"2026-10-03 09:00":          time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC),
		"11:00":                     time.Date(2026, 10, 2, 11, 0, 0, 0, time.UTC),
		"09:00":                     time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC),
		"2026-10-03T09:00:00+08:00": time.Date(2026, 10, 3, 1, 0, 0, 0, time.UTC),
	} {
		got, err := parseAt(in, now)
		if err != nil || !got.Equal(want) {
			t.Errorf("parseAt(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
}
