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
	"github.com/thisisnic/tasktracker/internal/update"
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

// New builds the root command. start begins the check for a newer
// release and returns a function that waits for its answer; Execute
// passes update.Start. Nil means no check, which is what tests want.
func New(start func(context.Context) func() string) *cobra.Command {
	var dbPath, cfgPath string
	var port int
	var noOpen bool
	// newer waits for the release check once one has started. The TUI
	// shows its answer in its title line, and the root's post-run hook
	// says it after every other command.
	newer := func() string { return "" }
	root := &cobra.Command{
		Use:   "tasktracker",
		Short: "A personal tracker for projects, tasks and subtasks",
		Long: `tasktracker is a local tracker for projects, tasks and subtasks, with
areas above projects to group them.

Run it with no arguments to serve the browser UI on a loopback port and
open it; tasktracker tui opens the terminal UI instead. Subcommands give
the same data a scriptable interface; add --json to any list or show
command for machine-readable output. The browser UI's JSON API is under
/api/ on the same port.

A project can link to goals in goaltracker. tasktracker reads goaltracker's
database read-only to show their statements; set [goaltracker] db in the
config if it is not in the usual place.

Backups are encrypted snapshots written to a folder you choose. Run
tasktracker key new once to set that up; with on_quit set in the config the
TUI writes one when it exits, unless nothing changed while it was open.
While serving, the database is backed up when it has changed: once at
start and then every 12 hours, if [backup] is configured.

Once a day tasktracker asks GitHub for the latest release. While a newer one
is out, every command says so on stderr and the TUI says so in its title
line; tasktracker update installs it.`,
		Version:       version.String(),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServe(cmd, dbPath, cfgPath, port, noOpen)
		},
		// The check starts once the command to run is known, so that shell
		// completion, which is read by the shell and must not touch the
		// network, and the update command, which asks GitHub itself, start
		// none. It runs in the background from there, so the command's
		// own work overlaps the network call.
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			if start != nil && !isCompletion(cmd) && cmd.Name() != "update" {
				newer = start(cmd.Context())
			}
		},
		// After the command's output, so the notice is the last thing on
		// the screen. A command that failed gets no hook and no notice:
		// its error is what wants reading.
		PersistentPostRun: func(cmd *cobra.Command, args []string) {
			updateNotice(cmd.ErrOrStderr(), newer)
		},
	}
	root.PersistentFlags().StringVar(&dbPath, "db", DefaultDBPath(), "path to the SQLite database (env TASKTRACKER_DB)")
	root.PersistentFlags().StringVar(&cfgPath, "config", config.Path(), "path to the config file")
	root.Flags().IntVar(&port, "port", 0, "port to serve the browser UI on (overrides the config)")
	root.Flags().BoolVar(&noOpen, "no-open", false, "do not open a browser")
	root.SetVersionTemplate("tasktracker {{.Version}}\n")
	root.AddCommand(tuiCmd(&dbPath, &cfgPath, &newer), areaCmd(&dbPath), projectCmd(&dbPath, &cfgPath), taskCmd(&dbPath), subtaskCmd(&dbPath), keyCmd(), backupCmd(&dbPath, &cfgPath), restoreCmd(&dbPath, &cfgPath), versionCmd(), updateCmd())
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
	return goalsFor(cfg, err)
}

// goalsFor picks the goaltracker database from a loaded config: the one it
// names, or goaltracker's own default location when it names none or
// could not be read (err is config.Load's error).
func goalsFor(cfg config.Config, err error) *goallink.Reader {
	if err == nil && cfg.Goaltracker.DB != "" {
		return goallink.New(cfg.Goaltracker.DB)
	}
	return goallink.New(goallink.DefaultPath())
}

// Execute runs the root command and exits non-zero on error.
func Execute() {
	root := New(func(ctx context.Context) func() string {
		return update.Start(ctx, update.CachePath(), version.String()).Wait
	})
	if err := root.ExecuteContext(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "tasktracker:", err)
		os.Exit(1)
	}
}

// updateNotice writes one line to w when newer, the wait on the release
// check, answers with a release newer than the one running. The wait is
// bounded by the check's own timeout.
func updateNotice(w io.Writer, newer func() string) {
	if latest := newer(); latest != "" {
		fmt.Fprintf(w, "tasktracker %s is out; this is %s. Run tasktracker update to install it.\n", latest, strings.TrimPrefix(version.String(), "v"))
	}
}

// isCompletion reports whether cmd is cobra's shell completion: the
// hidden command a shell calls for each Tab (__completeNoDesc is an
// alias of it, with the same name), or the completion command and its
// subcommands that print the scripts.
func isCompletion(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		switch c.Name() {
		case cobra.ShellCompRequestCmd, "completion":
			return true
		}
	}
	return false
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
