package cron

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func job(t *testing.T, name, schedule string) Job {
	return Job{Name: name, Schedule: schedule, Prompt: "do " + name, CWD: t.TempDir(), Created: time.Now()}
}

func TestDirAddListRemove(t *testing.T) {
	d := Dir(filepath.Join(t.TempDir(), "cron"))
	if err := d.Add(job(t, "b", "@daily"), false); err != nil {
		t.Fatal(err)
	}
	once := job(t, "a", "")
	once.At = time.Now().Add(time.Hour)
	if err := d.Add(once, false); err != nil {
		t.Fatal(err)
	}
	jobs, errs := d.List()
	if len(errs) != 0 || len(jobs) != 2 || jobs[0].Name != "a" || jobs[1].Name != "b" {
		t.Fatalf("List = %+v, %v", jobs, errs)
	}
	if !jobs[0].At.Equal(once.At) {
		t.Fatalf("one-shot time = %v, want %v", jobs[0].At, once.At)
	}
	if err := d.Remove("b"); err != nil {
		t.Fatal(err)
	}
	if err := d.Remove("b"); err == nil {
		t.Fatal("removed a job twice")
	}
	if jobs, _ := d.List(); len(jobs) != 1 {
		t.Fatalf("List after remove = %+v", jobs)
	}
}

func TestDirAddKeepsNamesUnique(t *testing.T) {
	d := Dir(t.TempDir())
	if err := d.Add(job(t, "x", "@daily"), false); err != nil {
		t.Fatal(err)
	}
	if err := d.Add(job(t, "x", "@hourly"), false); err == nil || !strings.Contains(err.Error(), "--replace") {
		t.Fatalf("second add: %v", err)
	}
	if err := d.Add(job(t, "x", "@hourly"), true); err != nil {
		t.Fatal(err)
	}
	jobs, _ := d.List()
	if len(jobs) != 1 || jobs[0].Schedule != "@hourly" {
		t.Fatalf("List = %+v", jobs)
	}
	// Nothing but the job is left behind.
	entries, _ := os.ReadDir(string(d))
	if len(entries) != 1 {
		t.Fatalf("dir holds %d entries", len(entries))
	}
}

func TestDirAddValidates(t *testing.T) {
	d := Dir(t.TempDir())
	both := job(t, "both", "@daily")
	both.At = time.Now()
	for name, j := range map[string]Job{
		"bad name":     job(t, "Bad Name", "@daily"),
		"path name":    job(t, "../x", "@daily"),
		"bad schedule": job(t, "x", "every day"),
		"no schedule":  job(t, "x", ""),
		"both":         both,
		"no prompt":    {Name: "x", Schedule: "@daily", CWD: t.TempDir()},
		"relative cwd": {Name: "x", Schedule: "@daily", Prompt: "p", CWD: "work"},
		"unknown kind": {Name: "x", Kind: "alarm", Schedule: "@daily", Prompt: "p", CWD: t.TempDir()},
	} {
		if err := d.Add(j, false); err == nil {
			t.Errorf("%s: added", name)
		}
	}
}

func TestDirListSkipsBadFiles(t *testing.T) {
	d := Dir(t.TempDir())
	d.Add(job(t, "good", "@daily"), false)
	os.WriteFile(filepath.Join(string(d), "broken.json"), []byte("{"), 0o600)
	os.WriteFile(filepath.Join(string(d), "renamed.json"), []byte(`{"name":"other","schedule":"@daily","prompt":"p","cwd":"/"}`), 0o600)
	os.WriteFile(filepath.Join(string(d), ".add-123"), []byte("partial"), 0o600)
	jobs, errs := d.List()
	if len(jobs) != 1 || jobs[0].Name != "good" || len(errs) != 2 {
		t.Fatalf("List = %+v, %v", jobs, errs)
	}
}
