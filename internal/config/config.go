// Package config loads inari's configuration: how to run kon, where inari keeps
// its state, and one section per chat connector.
//
// Every connector shares the same building blocks, Access and Routes, so a
// new connector adds a section to Connectors and reuses them.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Config is the whole of config.json.
type Config struct {
	Kon Kon `json:"kon"`
	// StateDir holds inari's own state, such as which kon session each chat
	// belongs to. It defaults to $XDG_STATE_HOME/inari.
	StateDir   string     `json:"state_dir"`
	Connectors Connectors `json:"connectors"`
}

// Kon is how inari starts the agent.
type Kon struct {
	// Command is the kon executable, found on PATH unless it is a path.
	Command string `json:"command"`
	// Args are its arguments; they default to ["acp"].
	Args []string `json:"args"`
}

// Connectors has one section per chat platform. A nil section is a connector
// that does not run.
type Connectors struct {
	Discord *Discord `json:"discord"`
}

// Discord configures the Discord connector.
type Discord struct {
	// Token is the bot token. TokenEnv names an environment variable to read
	// it from instead, which keeps the secret out of the file.
	Token    string `json:"token"`
	TokenEnv string `json:"token_env"`
	// Guilds lists servers to register slash commands in, where they appear
	// at once. With none, commands are registered globally.
	Guilds []string `json:"guilds"`
	Access Access   `json:"access"`
	Routes
}

// Access is who may talk to kon through a connector. kon runs commands
// without asking, so anything not listed is refused.
type Access struct {
	// Users are trusted wherever kon is reachable.
	Users []string `json:"users"`
	// Channels trust everyone in them.
	Channels []string `json:"channels"`
}

// Allows reports whether user may use kon in channel.
func (a Access) Allows(user, channel string) bool {
	return slices.Contains(a.Users, user) || slices.Contains(a.Channels, channel)
}

// Routes is where each channel's kon session works and what it is told. A
// channel's session is shared by everyone in it.
type Routes struct {
	// Channels maps a channel ID to its settings.
	Channels map[string]Channel `json:"channels"`
	// DefaultCWD is the working directory of a channel with none of its own.
	// Empty, such a channel is ignored.
	DefaultCWD string `json:"default_cwd"`
	// Instructions are told to every channel's new sessions, after
	// inari's own and before the channel's.
	Instructions string `json:"instructions"`
}

// Channel is one channel's settings.
type Channel struct {
	CWD string `json:"cwd"`
	// Instructions are told to the channel's new sessions, such as what the
	// channel is for.
	Instructions string `json:"instructions"`
}

// CWD is channel's working directory, or "" when kon does not serve it.
func (r Routes) CWD(channel string) string {
	if c, ok := r.Channels[channel]; ok && c.CWD != "" {
		return c.CWD
	}
	return r.DefaultCWD
}

// InstructionsFor are what channel's new sessions are told from the config.
func (r Routes) InstructionsFor(channel string) string {
	return strings.TrimSpace(r.Instructions + "\n\n" + r.Channels[channel].Instructions)
}

func (r Routes) validate() error {
	dirs := []string{r.DefaultCWD}
	for _, c := range r.Channels {
		dirs = append(dirs, c.CWD)
	}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		if !filepath.IsAbs(dir) {
			return fmt.Errorf("working directory %q is not absolute", dir)
		}
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			return fmt.Errorf("working directory %q is not a directory", dir)
		}
	}
	return nil
}

// DefaultPath is where inari looks for its config: $XDG_CONFIG_HOME/inari.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "inari", "config.json"), nil
}

// Load reads, defaults, and validates the config at path. Unknown fields are
// errors, so a typo does not silently loosen access.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c, err := parse(data)
	if err == nil {
		err = c.finish()
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

func parse(data []byte) (*Config, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) finish() error {
	if c.Kon.Command == "" {
		c.Kon.Command = "kon"
	}
	if c.Kon.Args == nil {
		c.Kon.Args = []string{"acp"}
	}
	if c.StateDir == "" {
		dir, err := stateHome()
		if err != nil {
			return err
		}
		c.StateDir = filepath.Join(dir, "inari")
	}
	if c.Connectors == (Connectors{}) {
		return errors.New("no connector is configured")
	}
	if d := c.Connectors.Discord; d != nil {
		if d.Token == "" && d.TokenEnv != "" {
			d.Token = os.Getenv(d.TokenEnv)
		}
		if d.Token == "" {
			return errors.New("discord: no token; set token or token_env")
		}
		if err := d.Routes.validate(); err != nil {
			return fmt.Errorf("discord: %w", err)
		}
	}
	return nil
}

func stateHome() (string, error) {
	if dir := os.Getenv("XDG_STATE_HOME"); filepath.IsAbs(dir) {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state"), nil
}
