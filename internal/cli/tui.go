package cli

import (
	"github.com/spf13/cobra"
)

// runTUI is what the bare command does. Until the terminal UI lands it
// shows the tree, the same as task list.
func runTUI(cmd *cobra.Command, dbPath, cfgPath string) error {
	store, err := openStore(&dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	tree, err := store.Tree(cmd.Context(), false)
	if err != nil {
		return err
	}
	printTree(cmd.OutOrStdout(), tree)
	return nil
}
