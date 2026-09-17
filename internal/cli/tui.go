package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/thisisnic/tasktracker/internal/config"
	"github.com/thisisnic/tasktracker/internal/tui"
)

// runTUI is what the bare command does: open the terminal UI, then back
// up on the way out if the config asks for it.
func runTUI(cmd *cobra.Command, dbPath, cfgPath string) error {
	// The config only affects backups and the goal lookup, so a broken one
	// must not keep the UI from opening. It is reported on exit instead.
	cfg, cfgErr := config.Load(cfgPath)
	store, err := openStore(&dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := tui.Run(cmd.Context(), store, tui.Options{Goals: goalReader(cmd, cfgPath)}); err != nil {
		return err
	}
	if cfgErr != nil {
		return fmt.Errorf("no backup on quit: %w", cfgErr)
	}
	if cfg.Backup.OnQuit && cfg.Backup.Configured() {
		return runBackup(cmd, store, dbPath, cfg.Backup)
	}
	return nil
}
