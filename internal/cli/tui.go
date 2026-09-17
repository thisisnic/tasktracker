package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/thisisnic/tasktracker/internal/config"
	"github.com/thisisnic/tasktracker/internal/task"
	"github.com/thisisnic/tasktracker/internal/tui"
)

// runTUI is what the bare command does: open the terminal UI, then back
// up on the way out if the config asks for it.
func runTUI(cmd *cobra.Command, dbPath, cfgPath string) error {
	// The config only affects backups and the goal lookup, so a broken one
	// must not keep the UI from opening. It is reported on exit instead,
	// once: the goal reader falls back quietly rather than printing a
	// note the alt screen would hide anyway.
	cfg, cfgErr := config.Load(cfgPath)
	goals := goalsFor(cfg, cfgErr)
	store, err := openStore(&dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := tui.Run(cmd.Context(), store, tui.Options{Goals: goals}); err != nil {
		return err
	}
	return afterQuit(cmd, store, dbPath, cfg, cfgErr)
}

// afterQuit is what happens once the UI has closed: a backup when the
// config asks for one, or the config's own error when it could not be read.
func afterQuit(cmd *cobra.Command, store *task.Store, dbPath string, cfg config.Config, cfgErr error) error {
	if cfgErr != nil {
		return fmt.Errorf("no backup on quit: %w", cfgErr)
	}
	if cfg.Backup.OnQuit && cfg.Backup.Configured() {
		return runBackup(cmd, store, dbPath, cfg.Backup)
	}
	return nil
}
