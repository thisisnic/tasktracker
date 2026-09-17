package cli

import (
	"errors"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/thisisnic/tasktracker/internal/task"
)

func taskCmd(dbPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "task",
		Short: "Add, list and update tasks",
	}
	cmd.AddCommand(
		taskAddCmd(dbPath),
		taskListCmd(dbPath),
		taskShowCmd(dbPath),
		taskEditCmd(dbPath),
		taskMarkCmd(dbPath),
		taskDeleteCmd(dbPath),
	)
	return cmd
}

func taskAddCmd(dbPath *string) *cobra.Command {
	var in task.NewTask
	var asJSON bool
	cmd := &cobra.Command{
		Use:     "add TITLE --project ID",
		Short:   "Add a task to a project",
		Example: `  tasktracker task add "paint the hall" --project 1 --due 2026-10-01`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openStore(dbPath)
			if err != nil {
				return err
			}
			defer store.Close()
			in.Title = args[0]
			t, err := store.AddTask(cmd.Context(), in)
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), t)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "added task %d: %s (project %d)\n", t.ID, t.Title, t.ProjectID)
			return nil
		},
	}
	cmd.Flags().Int64Var(&in.ProjectID, "project", 0, "id of the project the task belongs to (required)")
	cmd.Flags().StringVar(&in.Due, "due", "", "due date: YYYY-MM-DD, today or tomorrow")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the task as JSON")
	_ = cmd.MarkFlagRequired("project")
	return cmd
}

func taskListCmd(dbPath *string) *cobra.Command {
	var f task.TaskFilter
	var status string
	var all, asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List tasks",
		Long: `List open tasks under their projects. --all includes done and dropped tasks
and finished projects. --due lists only tasks with a due date, soonest first.
--project or --status narrow the list.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if status != "" {
				st, err := task.ParseStatus(status)
				if err != nil {
					return err
				}
				f.Status = st
			}
			store, err := openStore(dbPath)
			if err != nil {
				return err
			}
			defer store.Close()
			filtered := f.ProjectID != 0 || f.Status != "" || f.Due
			if !filtered {
				outline, err := store.Outline(cmd.Context(), all)
				if err != nil {
					return err
				}
				if asJSON {
					if outline.Areas == nil {
						outline.Areas = []task.AreaNode{}
					}
					if outline.Projects == nil {
						outline.Projects = []task.ProjectNode{}
					}
					return writeJSON(cmd.OutOrStdout(), outline)
				}
				printTree(cmd.OutOrStdout(), outline)
				return nil
			}
			f.Open = !all && f.Status == ""
			tasks, err := store.ListTasks(cmd.Context(), f)
			if err != nil {
				return err
			}
			if asJSON {
				if tasks == nil {
					tasks = []task.Task{}
				}
				return writeJSON(cmd.OutOrStdout(), tasks)
			}
			if len(tasks) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no tasks match")
				return nil
			}
			printTasks(cmd, tasks)
			return nil
		},
	}
	cmd.Flags().Int64Var(&f.ProjectID, "project", 0, "only tasks in this project")
	cmd.Flags().StringVar(&status, "status", "", "only tasks with this status: todo, doing, done or dropped")
	cmd.Flags().BoolVar(&f.Due, "due", false, "only tasks with a due date, soonest first")
	cmd.Flags().BoolVar(&all, "all", false, "include done and dropped tasks")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print as JSON")
	return cmd
}

func printTasks(cmd *cobra.Command, tasks []task.Task) {
	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tPROJECT\tSTATUS\tDUE\tTITLE")
	today := time.Now()
	for _, t := range tasks {
		due := t.Due
		if t.Overdue(today) {
			due += " overdue"
		}
		fmt.Fprintf(tw, "%d\t%d\t%s\t%s\t%s\n", t.ID, t.ProjectID, t.Status, due, t.Title)
	}
	tw.Flush()
}

func taskShowCmd(dbPath *string) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "show ID",
		Short: "Show a task with its subtasks",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseID("task", args[0])
			if err != nil {
				return err
			}
			store, err := openStore(dbPath)
			if err != nil {
				return err
			}
			defer store.Close()
			t, err := store.GetTask(cmd.Context(), id)
			if err != nil {
				return fmt.Errorf("task %d: %w", id, err)
			}
			subs, err := store.ListSubtasks(cmd.Context(), id)
			if err != nil {
				return err
			}
			if asJSON {
				if subs == nil {
					subs = []task.Subtask{}
				}
				return writeJSON(cmd.OutOrStdout(), task.TaskNode{Task: t, Subtasks: subs})
			}
			p, err := store.GetProject(cmd.Context(), t.ProjectID)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "#%d  %s\n", t.ID, t.Title)
			fmt.Fprintf(out, "project: #%d %s\n", p.ID, p.Name)
			fmt.Fprintf(out, "status:  %s\n", t.Status)
			if t.Due != "" {
				line := t.Due
				if days, ok := t.DaysUntilDue(time.Now()); ok && t.Open() {
					line += "  " + dueText(days)
				}
				fmt.Fprintf(out, "due:     %s\n", line)
			}
			if len(subs) > 0 {
				fmt.Fprintln(out, "subtasks:")
				for _, s := range subs {
					box := "[ ]"
					if s.Done {
						box = "[x]"
					}
					fmt.Fprintf(out, "  %s %d  %s\n", box, s.ID, s.Title)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print as JSON")
	return cmd
}

// dueText says how far off a due date is, in words.
func dueText(days int) string {
	switch {
	case days < -1:
		return fmt.Sprintf("%d days overdue", -days)
	case days == -1:
		return "1 day overdue"
	case days == 0:
		return "today"
	case days == 1:
		return "tomorrow"
	}
	return fmt.Sprintf("in %d days", days)
}

func taskEditCmd(dbPath *string) *cobra.Command {
	var title, due string
	var project int64
	var noDue bool
	cmd := &cobra.Command{
		Use:   "edit ID",
		Short: "Change a task's title, due date or project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseID("task", args[0])
			if err != nil {
				return err
			}
			var e task.TaskEdit
			if cmd.Flags().Changed("title") {
				e.Title = &title
			}
			switch {
			case noDue && cmd.Flags().Changed("due"):
				return errors.New("--due and --no-due cannot both be given")
			case noDue:
				none := ""
				e.Due = &none
			case cmd.Flags().Changed("due"):
				e.Due = &due
			}
			if cmd.Flags().Changed("project") {
				e.ProjectID = &project
			}
			if e == (task.TaskEdit{}) {
				return errors.New("nothing to change; give at least one flag")
			}
			store, err := openStore(dbPath)
			if err != nil {
				return err
			}
			defer store.Close()
			t, err := store.UpdateTask(cmd.Context(), id, e)
			if err != nil {
				return fmt.Errorf("task %d: %w", id, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "updated task %d: %s\n", t.ID, t.Title)
			return nil
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "new title")
	cmd.Flags().StringVar(&due, "due", "", "new due date: YYYY-MM-DD, today or tomorrow")
	cmd.Flags().BoolVar(&noDue, "no-due", false, "remove the due date")
	cmd.Flags().Int64Var(&project, "project", 0, "move the task to this project")
	return cmd
}

func taskMarkCmd(dbPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "mark ID todo|doing|done|dropped",
		Short: "Set a task's status",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseID("task", args[0])
			if err != nil {
				return err
			}
			st, err := task.ParseStatus(args[1])
			if err != nil {
				return err
			}
			store, err := openStore(dbPath)
			if err != nil {
				return err
			}
			defer store.Close()
			if err := store.MarkTask(cmd.Context(), id, st); err != nil {
				return fmt.Errorf("task %d: %w", id, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "task %d marked %s\n", id, st)
			return nil
		},
	}
}

func taskDeleteCmd(dbPath *string) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "delete ID",
		Short: "Delete a task and its subtasks",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseID("task", args[0])
			if err != nil {
				return err
			}
			store, err := openStore(dbPath)
			if err != nil {
				return err
			}
			defer store.Close()
			t, err := store.GetTask(cmd.Context(), id)
			if err != nil {
				return fmt.Errorf("task %d: %w", id, err)
			}
			if !yes && !confirm(cmd, fmt.Sprintf("delete task %d %q and its subtasks?", t.ID, t.Title)) {
				fmt.Fprintln(cmd.OutOrStdout(), "kept")
				return nil
			}
			if err := store.DeleteTask(cmd.Context(), id); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "deleted task %d\n", id)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip the confirmation prompt")
	return cmd
}
