// Package cli defines the tasktracker command tree.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/thisisnic/tasktracker/internal/config"
	"github.com/thisisnic/tasktracker/internal/goallink"
	"github.com/thisisnic/tasktracker/internal/task"
	"github.com/thisisnic/tasktracker/internal/version"
)

// DefaultDBPath is where the database lives unless overridden by --db or
// the TASKTRACKER_DB environment variable: $XDG_DATA_HOME/tasktracker/tasktracker.db,
// falling back to ~/.local/share/tasktracker/tasktracker.db.
func DefaultDBPath() string {
	if p := os.Getenv("TASKTRACKER_DB"); p != "" {
		return p
	}
	return dataDirDBPath()
}

// dataDirDBPath is the tool's own location for the database, ignoring
// TASKTRACKER_DB. Only a database here lives in a directory the tool owns.
func dataDirDBPath() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "tasktracker", "tasktracker.db")
}

// New builds the root command.
func New() *cobra.Command {
	var dbPath, cfgPath string
	root := &cobra.Command{
		Use:   "tasktracker",
		Short: "A personal tracker for projects, tasks and subtasks",
		Long: `tasktracker is a local tracker for projects, tasks and subtasks.

Run it with no arguments to open the terminal UI. Subcommands give the same
data a scriptable interface; add --json to any list or show command for
machine-readable output.

A project can link to goals in goaltracker. tasktracker reads goaltracker's
database read-only to show their statements; set [goaltracker] db in the
config if it is not in the usual place.

Backups are encrypted snapshots written to a folder you choose. Run
tasktracker key new once to set that up; with on_quit set in the config the
TUI writes one every time it exits.`,
		Version:       version.String(),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTUI(cmd, dbPath, cfgPath)
		},
	}
	root.PersistentFlags().StringVar(&dbPath, "db", DefaultDBPath(), "path to the SQLite database (env TASKTRACKER_DB)")
	root.PersistentFlags().StringVar(&cfgPath, "config", config.Path(), "path to the config file")
	root.SetVersionTemplate("tasktracker {{.Version}}\n")
	root.AddCommand(projectCmd(&dbPath, &cfgPath), taskCmd(&dbPath), subtaskCmd(&dbPath), keyCmd(), backupCmd(&dbPath, &cfgPath), restoreCmd(&dbPath, &cfgPath), versionCmd(), updateCmd())
	return root
}

// openDB opens the database, vouching for its directory only when it is
// the tool's own default location.
func openDB(path string) (*task.Store, error) {
	if path == dataDirDBPath() {
		return task.Open(path, task.OwnDir())
	}
	return task.Open(path)
}

func openStore(path *string) (*task.Store, error) {
	return openDB(*path)
}

// goalReader looks goals up in goaltracker's database: the one named in
// the config, or goaltracker's own default location. A config that cannot
// be read is reported, since it may be the one naming the database, and
// the default location is used.
func goalReader(cmd *cobra.Command, cfgPath string) *goallink.Reader {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "note: config: %v; using goaltracker's default database\n", err)
	}
	if cfg.Goaltracker.DB != "" {
		return goallink.New(cfg.Goaltracker.DB)
	}
	return goallink.New(goallink.DefaultPath())
}

// Execute runs the root command and exits non-zero on error.
func Execute() {
	root := New()
	if err := root.ExecuteContext(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "tasktracker:", err)
		os.Exit(1)
	}
}

func parseID(what, s string) (int64, error) {
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("%s id %q: want a positive integer", what, s)
	}
	return id, nil
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// confirm asks a yes/no question on stdout and reads one line from stdin.
func confirm(cmd *cobra.Command, prompt string) bool {
	fmt.Fprint(cmd.OutOrStdout(), prompt+" [y/N] ")
	var answer string
	fmt.Fscanln(cmd.InOrStdin(), &answer)
	return strings.HasPrefix(strings.ToLower(answer), "y")
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintf(cmd.OutOrStdout(), "tasktracker %s\n", version.String())
		},
	}
}
