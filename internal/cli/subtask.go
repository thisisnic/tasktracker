package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func subtaskCmd(dbPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "subtask",
		Short: "Add, tick and remove checklist items under a task",
	}
	cmd.AddCommand(
		subtaskAddCmd(dbPath),
		subtaskTickCmd(dbPath, true),
		subtaskTickCmd(dbPath, false),
		subtaskEditCmd(dbPath),
		subtaskDeleteCmd(dbPath),
	)
	return cmd
}

func subtaskAddCmd(dbPath *string) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:     "add TASK_ID TITLE",
		Short:   "Add a checklist item to a task",
		Example: `  tasktracker subtask add 4 "buy paint"`,
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			taskID, err := parseID("task", args[0])
			if err != nil {
				return err
			}
			store, err := openStore(dbPath)
			if err != nil {
				return err
			}
			defer store.Close()
			s, err := store.AddSubtask(cmd.Context(), taskID, args[1])
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), s)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "added subtask %d: %s (task %d)\n", s.ID, s.Title, s.TaskID)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the subtask as JSON")
	return cmd
}

func subtaskTickCmd(dbPath *string, done bool) *cobra.Command {
	use, short, verb := "tick ID", "Tick a checklist item", "ticked"
	if !done {
		use, short, verb = "untick ID", "Untick a checklist item", "unticked"
	}
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseID("subtask", args[0])
			if err != nil {
				return err
			}
			store, err := openStore(dbPath)
			if err != nil {
				return err
			}
			defer store.Close()
			if err := store.TickSubtask(cmd.Context(), id, done); err != nil {
				return fmt.Errorf("subtask %d: %w", id, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "subtask %d %s\n", id, verb)
			return nil
		},
	}
}

func subtaskEditCmd(dbPath *string) *cobra.Command {
	var title string
	cmd := &cobra.Command{
		Use:   "edit ID --title TITLE",
		Short: "Rename a checklist item",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseID("subtask", args[0])
			if err != nil {
				return err
			}
			store, err := openStore(dbPath)
			if err != nil {
				return err
			}
			defer store.Close()
			s, err := store.RenameSubtask(cmd.Context(), id, title)
			if err != nil {
				return fmt.Errorf("subtask %d: %w", id, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "updated subtask %d: %s\n", s.ID, s.Title)
			return nil
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "new title (required)")
	_ = cmd.MarkFlagRequired("title")
	return cmd
}

func subtaskDeleteCmd(dbPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "delete ID",
		Short: "Remove a checklist item",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseID("subtask", args[0])
			if err != nil {
				return err
			}
			store, err := openStore(dbPath)
			if err != nil {
				return err
			}
			defer store.Close()
			if err := store.DeleteSubtask(cmd.Context(), id); err != nil {
				return fmt.Errorf("subtask %d: %w", id, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "deleted subtask %d\n", id)
			return nil
		},
	}
}
