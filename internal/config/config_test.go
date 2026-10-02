package config

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadDefaultsAndTokenEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("INARI_TEST_TOKEN", "secret")
	t.Setenv("XDG_STATE_HOME", dir)
	c, err := Load(write(t, `{"connectors":{"discord":{"token_env":"INARI_TEST_TOKEN","default_cwd":"`+dir+`"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Kon.Command != "kon" || len(c.Kon.Args) != 1 || c.Kon.Args[0] != "acp" {
		t.Fatalf("kon = %+v", c.Kon)
	}
	if c.StateDir != filepath.Join(dir, "inari") {
		t.Fatalf("state dir = %q", c.StateDir)
	}
	if c.Connectors.Discord.Token != "secret" {
		t.Fatalf("token = %q", c.Connectors.Discord.Token)
	}
}

func TestDefaultPathFollowsXDG(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if got, _ := DefaultPath(); got != filepath.Join(dir, "inari", "config.json") {
		t.Fatalf("DefaultPath = %q", got)
	}
	// A relative value is not a base directory, as the XDG spec says.
	t.Setenv("XDG_CONFIG_HOME", "relative")
	if got, _ := DefaultPath(); !filepath.IsAbs(got) {
		t.Fatalf("DefaultPath = %q, want an absolute path", got)
	}
}

func TestLoadRejects(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"unknown field":   `{"connectors":{"discord":{"token":"x","acess":{}}}}`,
		"no connector":    `{}`,
		"no token":        `{"connectors":{"discord":{}}}`,
		"relative cwd":    `{"connectors":{"discord":{"token":"x","default_cwd":"work"}}}`,
		"missing channel": `{"connectors":{"discord":{"token":"x","channels":{"1":{"cwd":"` + dir + `/nope"}}}}}`,
	} {
		if _, err := Load(write(t, body)); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
}

func TestAccessDeniesByDefault(t *testing.T) {
	a := Access{Users: []string{"u1"}, Channels: []string{"c1"}}
	cases := []struct {
		user, channel string
		want          bool
	}{
		{"u1", "c9", true},
		{"u9", "c1", true},
		{"u9", "c9", false},
	}
	for _, c := range cases {
		if got := a.Allows(c.user, c.channel); got != c.want {
			t.Errorf("Allows(%s, %s) = %v", c.user, c.channel, got)
		}
	}
	if (Access{}).Allows("u1", "c1") {
		t.Error("empty access allows")
	}
}

func TestRoutesPreferChannelCWD(t *testing.T) {
	r := Routes{Channels: map[string]Channel{"c1": {CWD: "/a"}}, DefaultCWD: "/d"}
	if r.CWD("c1") != "/a" || r.CWD("c2") != "/d" {
		t.Fatalf("CWD = %q, %q", r.CWD("c1"), r.CWD("c2"))
	}
	r.Instructions = "be brief"
	r.Channels["c1"] = Channel{CWD: "/a", Instructions: "this is #ops"}
	if got := r.InstructionsFor("c1"); got != "be brief\n\nthis is #ops" {
		t.Fatalf("InstructionsFor(c1) = %q", got)
	}
	if got := r.InstructionsFor("c2"); got != "be brief" {
		t.Fatalf("InstructionsFor(c2) = %q", got)
	}
	if (Routes{}).CWD("c1") != "" {
		t.Fatal("a channel with no directory is served")
	}
}

// TestExampleConfigParses keeps the example in step with the config's keys.
// Its directories are placeholders, so only its shape is checked.
func TestExampleConfigParses(t *testing.T) {
	data, err := os.ReadFile("../../config.example.json")
	if err != nil {
		t.Fatal(err)
	}
	c, err := parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if c.Connectors.Discord == nil {
		t.Fatal("example configures no discord connector")
	}
}

func TestCronHome(t *testing.T) {
	dir := t.TempDir()
	base := `"connectors":{"discord":{"token":"x","channels":{"1":{"cwd":"` + dir + `"}}}}`
	c, err := Load(write(t, `{`+base+`,"cron":{"home":"discord:1","timezone":"Asia/Singapore","timeout":"30m"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Cron.Location.String() != "Asia/Singapore" || c.Cron.Limit.Minutes() != 30 {
		t.Fatalf("cron = %+v", c.Cron)
	}
	for name, cron := range map[string]string{
		"unknown connector": `{"home":"telegram:1"}`,
		"no channel":        `{"home":"discord"}`,
		"unserved channel":  `{"home":"discord:2"}`,
		"bad timezone":      `{"home":"discord:1","timezone":"Mars/Olympus"}`,
		"bad timeout":       `{"home":"discord:1","timeout":"soon"}`,
	} {
		if _, err := Load(write(t, `{`+base+`,"cron":`+cron+`}`)); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
}

func TestLoadCronNeedsNoConnectors(t *testing.T) {
	c, err := LoadCron(write(t, `{"cron":{"home":"discord:1"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.StateDir == "" || c.Cron.Location == nil {
		t.Fatalf("cron config = %+v", c)
	}
}
