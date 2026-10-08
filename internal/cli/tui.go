package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/thisisnic/tasktracker/internal/config"
	"github.com/thisisnic/tasktracker/internal/task"
	"github.com/thisisnic/tasktracker/internal/tui"
)

// tuiCmd opens the terminal UI. newer points at the root's wait on the
// release check, which is set once the command runs, so it is read then
// and not when the command is built.
func tuiCmd(dbPath, cfgPath *string, newer *func() string) *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Open the terminal UI",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTUI(cmd, *dbPath, *cfgPath, *newer)
		},
	}
}

// runTUI opens the terminal UI, then backs up on the way out if the
// config asks for it. newer is the background check for a newer release,
// for the UI's title line.
func runTUI(cmd *cobra.Command, dbPath, cfgPath string, newer func() string) error {
	// The config only affects the backup on quit, so a broken one must
	// not keep the UI from opening. It is reported on exit instead, where
	// the alt screen would not hide it.
	cfg, cfgErr := config.Load(cfgPath)
	store, err := openStore(&dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	changed, err := tui.Run(cmd.Context(), store, tui.Options{Folds: tui.FoldsPath(dbPath), Newer: newer})
	if err != nil {
		return err
	}
	return afterQuit(cmd, store, dbPath, cfg, cfgErr, changed)
}

// afterQuit is what happens once the UI has closed: a backup when the
// config asks for one and the database changed while the UI was open, or
// the config's own error when it could not be read. A session that only
// looked has nothing new to keep, so it skips the snapshot; with git on
// it still pushes, in case an earlier push failed and left a commit
// waiting. `tasktracker backup` checks the database itself.
func afterQuit(cmd *cobra.Command, store *task.Store, dbPath string, cfg config.Config, cfgErr error, changed bool) error {
	if cfgErr != nil {
		return fmt.Errorf("no backup on quit: %w", cfgErr)
	}
	if !cfg.Backup.OnQuit || !cfg.Backup.Configured() {
		return nil
	}
	if !changed {
		fmt.Fprintln(cmd.OutOrStdout(), "backup: nothing changed this session")
		if cfg.Backup.Git && hasBackup(cfg.Backup.Dir) {
			return pushBackup(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), cfg.Backup)
		}
		return nil
	}
	return runBackup(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), store, dbPath, cfg.Backup)
}
