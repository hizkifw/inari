package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeSystemd records systemctl calls; active is what is-active reports.
func fakeSystemd(t *testing.T, active bool) (*systemd, *[]string) {
	t.Helper()
	var calls []string
	s := &systemd{
		unitDir: filepath.Join(t.TempDir(), "systemd", "user"),
		systemctl: func(args ...string) error {
			calls = append(calls, strings.Join(args, " "))
			if args[0] == "is-active" && !active {
				return errors.New("inactive")
			}
			return nil
		},
		lingering: func() bool { return false },
	}
	return s, &calls
}

func TestSystemdInstallUninstall(t *testing.T) {
	s, calls := fakeSystemd(t, false)
	notes, err := s.Install(Spec{
		Executable: "/home/a b/.local/bin/inari",
		Args:       []string{"-config", "/home/a b/50%/$HOME.json"},
		Env:        map[string]string{"PATH": "/usr/bin:/bin", "TOKEN": `t"o\k%$`},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"daemon-reload", "enable inari.service", "restart inari.service"}
	if !slices.Equal(*calls, want) {
		t.Fatalf("calls %q, want %q", *calls, want)
	}
	if !slices.ContainsFunc(notes, func(n string) bool { return strings.Contains(n, "enable-linger") }) {
		t.Fatalf("no linger note in %q", notes)
	}

	info, err := os.Stat(s.unitPath())
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Fatalf("unit mode %v, want 0644", perm)
	}
	data, _ := os.ReadFile(s.unitPath())
	for _, line := range []string{
		`ExecStart="/home/a b/.local/bin/inari" "-config" "/home/a b/50%%/$$HOME.json"`,
		`Environment="PATH=/usr/bin:/bin"`,
		`Environment="TOKEN=t\"o\\k%%$"`,
		"WantedBy=default.target",
	} {
		if !strings.Contains(string(data), line+"\n") {
			t.Errorf("unit lacks %s:\n%s", line, data)
		}
	}

	*calls = nil
	if err := s.Uninstall(); err != nil {
		t.Fatal(err)
	}
	if want := []string{"disable --now inari.service", "daemon-reload"}; !slices.Equal(*calls, want) {
		t.Fatalf("calls %q, want %q", *calls, want)
	}
	if _, err := os.Stat(s.unitPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unit still there: %v", err)
	}
	if err := s.Uninstall(); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("second uninstall: %v", err)
	}
}

func TestSystemdRestartOnlyWhenActive(t *testing.T) {
	s, calls := fakeSystemd(t, false)
	if restarted, err := s.Restart(); restarted || err != nil {
		t.Fatalf("inactive: restarted %v, %v", restarted, err)
	}
	if slices.ContainsFunc(*calls, func(c string) bool { return strings.Contains(c, "restart") }) {
		t.Fatalf("restarted an inactive service: %q", *calls)
	}

	s, calls = fakeSystemd(t, true)
	if restarted, err := s.Restart(); !restarted || err != nil {
		t.Fatalf("active: restarted %v, %v", restarted, err)
	}
	// Without --no-block, a restart from inside the service would kill
	// systemctl before it returned.
	if !slices.Contains(*calls, "--no-block restart inari.service") {
		t.Fatalf("calls %q", *calls)
	}
}
