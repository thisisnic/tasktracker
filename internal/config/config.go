// Package config reads tasktracker's optional config file.
//
// The file lives at $XDG_CONFIG_HOME/tasktracker/config.toml, falling back to
// ~/.config/tasktracker/config.toml. Everything in it is optional; without it the
// app works with no backups configured.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config is the whole config file.
type Config struct {
	Backup      Backup      `toml:"backup"`
	Goaltracker Goaltracker `toml:"goaltracker"`
}

// Goaltracker says where goaltracker keeps its database, so projects can
// show the statements of the goals they link to. Read-only.
type Goaltracker struct {
	// DB is the path to goaltracker's SQLite database. Empty means
	// goaltracker's own default: $GOALTRACKER_DB, or
	// $XDG_DATA_HOME/goaltracker/goaltracker.db.
	DB string `toml:"db"`
}

// Backup configures encrypted snapshots of the database.
type Backup struct {
	// Dir is where snapshots are written, typically a private git repo.
	Dir string `toml:"dir"`
	// Recipient is the age public key snapshots are encrypted to.
	Recipient string `toml:"recipient"`
	// IdentityFile holds the age private key, used only by restore.
	IdentityFile string `toml:"identity_file"`
	// OnQuit makes the TUI write a snapshot when it exits.
	OnQuit bool `toml:"on_quit"`
	// Git commits and pushes the backup file from Dir after each backup.
	Git bool `toml:"git"`
}

// Configured reports whether backups have somewhere to go and a key.
func (b Backup) Configured() bool { return b.Dir != "" && b.Recipient != "" }

// Dir returns the config directory.
func Dir() string {
	if base := os.Getenv("XDG_CONFIG_HOME"); base != "" {
		return filepath.Join(base, "tasktracker")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".config", "tasktracker")
}

// Path returns the config file path.
func Path() string { return filepath.Join(Dir(), "config.toml") }

// DefaultIdentityFile is where `tasktracker key new` writes the private key.
func DefaultIdentityFile() string { return filepath.Join(Dir(), "key.txt") }

// Load reads the config file. A missing file yields an empty Config.
func Load(path string) (Config, error) {
	var c Config
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if _, err := toml.Decode(string(data), &c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	c.Backup.Dir = ExpandHome(c.Backup.Dir)
	c.Backup.IdentityFile = ExpandHome(c.Backup.IdentityFile)
	c.Goaltracker.DB = ExpandHome(c.Goaltracker.DB)
	return c, nil
}

// ExpandHome replaces a leading ~ with the home directory.
func ExpandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

// Example is a config file with every field, for `tasktracker key new` to print.
func Example(recipient, identityFile string) string {
	return fmt.Sprintf(`[backup]
# Where encrypted snapshots go. Make this a private git repo of your own.
dir = "~/tasktracker-data"
# Your age public key. Snapshots are encrypted to it.
recipient = %q
# Your age private key file. Only restore reads it. Keep a copy somewhere
# safe outside this machine: without it the backups cannot be opened.
identity_file = %q
# Write a snapshot every time the TUI exits.
on_quit = true
# Set to true once dir is a git clone with a remote, and tasktracker will commit
# and push after each backup. Needs credentials that work without a prompt,
# such as an SSH key loaded in an agent.
git = false

[goaltracker]
# Where goaltracker keeps its database, read only to show the goals a
# project links to. Leave blank for goaltracker's own default location.
db = ""
`, recipient, identityFile)
}
