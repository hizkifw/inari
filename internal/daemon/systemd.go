package daemon

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
)

// systemd manages inari as a systemd user service, so it needs no root and
// runs as the user whose kon configuration it uses.
type systemd struct {
	unitDir string
	// systemctl runs `systemctl --user` with args.
	systemctl func(args ...string) error
	// lingering reports whether the user's services outlive their sessions.
	lingering func() bool
}

const unitName = Service + ".service"

// systemdBooted is sd_booted(3): the machine runs systemd when this directory
// exists.
func systemdBooted() bool {
	info, err := os.Stat("/run/systemd/system")
	return err == nil && info.IsDir()
}

func newSystemd() (*systemd, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(dir) {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		dir = filepath.Join(home, ".config")
	}
	return &systemd{
		unitDir:   filepath.Join(dir, "systemd", "user"),
		systemctl: systemctl,
		lingering: lingering,
	}, nil
}

func systemctl(args ...string) error {
	cmd := exec.Command("systemctl", append([]string{"--user"}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("systemctl --user %s: %s", strings.Join(args, " "), msg)
		}
		return fmt.Errorf("systemctl --user %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

func lingering() bool {
	u, err := user.Current()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join("/var/lib/systemd/linger", u.Username))
	return err == nil
}

func (s *systemd) Name() string { return "systemd" }

func (s *systemd) unitPath() string { return filepath.Join(s.unitDir, unitName) }

func (s *systemd) Install(spec Spec) ([]string, error) {
	if err := writeFile(s.unitPath(), []byte(unit(spec)), 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", s.unitPath(), err)
	}
	// restart, not start, so a reinstall runs the new unit.
	for _, args := range [][]string{{"daemon-reload"}, {"enable", unitName}, {"restart", unitName}} {
		if err := s.systemctl(args...); err != nil {
			return nil, err
		}
	}
	notes := []string{
		"unit: " + s.unitPath(),
		"logs: journalctl --user -u " + Service + " -f",
	}
	if !s.lingering() {
		notes = append(notes, "inari stops when you log out; to keep it running, run: loginctl enable-linger")
	}
	return notes, nil
}

func (s *systemd) Uninstall() error {
	if _, err := os.Stat(s.unitPath()); errors.Is(err, fs.ErrNotExist) {
		return ErrNotInstalled
	}
	if err := s.systemctl("disable", "--now", unitName); err != nil {
		return err
	}
	if err := os.Remove(s.unitPath()); err != nil {
		return err
	}
	return s.systemctl("daemon-reload")
}

func (s *systemd) Restart() (bool, error) {
	// is-active fails both when the unit is stopped and when it does not
	// exist, and neither needs a restart.
	if s.systemctl("is-active", "--quiet", unitName) != nil {
		return false, nil
	}
	if err := s.systemctl("--no-block", "restart", unitName); err != nil {
		return true, err
	}
	return true, nil
}

// unit renders the service file.
func unit(spec Spec) string {
	var b strings.Builder
	b.WriteString("# Written by `inari daemon install`; run it again to change this file.\n")
	b.WriteString("[Unit]\nDescription=inari: kon in your chat\nDocumentation=https://github.com/hizkifw/inari\n\n")
	b.WriteString("[Service]\nType=exec\n")
	words := []string{quoteExec(spec.Executable)}
	for _, a := range spec.Args {
		words = append(words, quoteExec(a))
	}
	fmt.Fprintf(&b, "ExecStart=%s\n", strings.Join(words, " "))
	for _, k := range slices.Sorted(maps.Keys(spec.Env)) {
		fmt.Fprintf(&b, "Environment=%s\n", quote(k+"="+spec.Env[k]))
	}
	// A failed connector exits inari, so a dropped gateway connection or a
	// network that is not up yet at boot is retried.
	b.WriteString("Restart=on-failure\nRestartSec=10\n\n")
	b.WriteString("[Install]\nWantedBy=default.target\n")
	return b.String()
}

// quote makes s one double-quoted word of a unit file setting, which reads
// C-style escapes and expands % specifiers.
func quote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "%", "%%")
	return `"` + r.Replace(s) + `"`
}

// quoteExec quotes a word of ExecStart, which also expands $ variables where
// Environment= does not.
func quoteExec(s string) string {
	return quote(strings.ReplaceAll(s, "$", "$$"))
}
