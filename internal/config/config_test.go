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
