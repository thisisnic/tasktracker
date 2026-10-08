package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadMissingIsDefaults(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil || c.Backup.Configured() || c.Port != DefaultPort {
		t.Fatalf("Load missing = %+v, %v", c, err)
	}
}

func TestLoadPort(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte("port = 8080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil || c.Port != 8080 {
		t.Fatalf("Load = %+v, %v", c, err)
	}
	if err := c.PortError(); err != nil {
		t.Errorf("PortError for 8080: %v", err)
	}
	// A port out of range is the server's problem alone: Load reads the
	// rest of the file as usual, paths expanded, so the TUI's backup is
	// not turned off by it.
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, bad := range []string{"port = 0\n", "port = 70000\n", "port = -1\n"} {
		if err := os.WriteFile(p, []byte(bad+"[backup]\ndir = \"~/data\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		c, err := Load(p)
		if err != nil || c.Backup.Dir != filepath.Join(home, "data") {
			t.Errorf("%q: Load = %+v, %v", bad, c, err)
		}
		if err := c.PortError(); err == nil || !strings.Contains(err.Error(), "port must be between 1 and 65535") {
			t.Errorf("%q: PortError = %v", bad, err)
		}
	}
	// A file broken after the port is read gives the default too.
	if err := os.WriteFile(p, []byte("port = 8080\n[backup\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, err := Load(p); err == nil || c.Port != DefaultPort {
		t.Errorf("broken after port: %+v, %v", c, err)
	}
}

func TestLoadExpandsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(Example("age1abc", "~/.config/tasktracker/key.txt")), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Backup.Dir != filepath.Join(home, "tasktracker-data") || c.Backup.IdentityFile != filepath.Join(home, ".config/tasktracker/key.txt") {
		t.Errorf("paths not expanded: %+v", c.Backup)
	}
	if c.Backup.Recipient != "age1abc" || !c.Backup.OnQuit || c.Backup.Git || !c.Backup.Configured() {
		t.Errorf("fields: %+v", c.Backup)
	}
}

func TestLoadBadTOML(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte("[backup\ndir = 1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Error("bad TOML accepted")
	}
}

func TestDirHonoursXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/x")
	if Dir() != "/x/tasktracker" || Path() != "/x/tasktracker/config.toml" {
		t.Errorf("Dir=%s Path=%s", Dir(), Path())
	}
}
