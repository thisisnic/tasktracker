package cli

import (
	"errors"
	"fmt"
	"strings"
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
		taskCopyCmd(dbPath),
		taskMarkCmd(dbPath),
		taskArchiveCmd(dbPath, true),
		taskArchiveCmd(dbPath, false),
		taskDeleteCmd(dbPath),
	)
	return cmd
}

func taskAddCmd(dbPath *string) *cobra.Command {
	var in task.NewTask
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "add TITLE --project ID",
		Short: "Add a task to a project",
		Example: `  tasktracker task add "paint the hall" --project 1 --due 2026-10-01
  tasktracker task add "fix the login bug" --project 2 --issue owner/repo#42`,
		Args: cobra.ExactArgs(1),
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
	cmd.Flags().StringVar(&in.Issue, "issue", "", "GitHub issue: a URL or owner/repo#N")
	cmd.Flags().StringVar(&in.Notes, "notes", "", "notes to keep with the task")
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
		Long: `List tasks under their projects. Finished tasks stay listed until they are
archived; --all includes archived tasks and finished projects, and --open
keeps only todo tasks. --due lists open tasks with a due date,
soonest first; add --all for finished ones too. --project or --status
narrow the list.`,
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
				if f.Open {
					outline = outline.WithoutFinished()
				}
				if asJSON {
					return writeJSON(cmd.OutOrStdout(), outline)
				}
				printTree(cmd.OutOrStdout(), outline)
				return nil
			}
			f.Unarchived = !all
			// The due list is for what is coming up, so on its own it
			// keeps to open tasks; --all or --status widens it.
			if f.Due && !all && f.Status == "" {
				f.Open = true
			}
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
	cmd.Flags().StringVar(&status, "status", "", "only tasks with this status: todo, done or dropped")
	cmd.Flags().BoolVar(&f.Due, "due", false, "only tasks with a due date, soonest first")
	cmd.Flags().BoolVar(&f.Open, "open", false, "only todo tasks")
	cmd.Flags().BoolVar(&all, "all", false, "include archived tasks and finished projects")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print as JSON")
	cmd.MarkFlagsMutuallyExclusive("open", "status")
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
		fmt.Fprintf(tw, "%d\t%d\t%s\t%s\t%s\n", t.ID, t.ProjectID, statusWord(t), due, t.Title)
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
			fmt.Fprintf(out, "status:  %s\n", statusWord(t))
			if t.Due != "" {
				line := t.Due
				if days, ok := t.DaysUntilDue(time.Now()); ok && t.Open() {
					line += "  " + dueText(days)
				}
				fmt.Fprintf(out, "due:     %s\n", line)
			}
			if t.Issue != "" {
				fmt.Fprintf(out, "issue:   %s\n", t.IssueText())
			}
			if t.Notes != "" {
				fmt.Fprintln(out, "notes:")
				for _, line := range strings.Split(t.Notes, "\n") {
					fmt.Fprintf(out, "  %s\n", line)
				}
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

// statusWord is a task's status for a listing, with archived after it
// when the task has been put away.
func statusWord(t task.Task) string {
	if t.Archived {
		return string(t.Status) + " (archived)"
	}
	return string(t.Status)
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
	var title, due, issue, notes string
	var project int64
	var noDue, noIssue, noNotes bool
	cmd := &cobra.Command{
		Use:   "edit ID",
		Short: "Change a task's title, due date, issue, notes or project",
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
			// Cobra rejects --issue with --no-issue, and --notes with
			// --no-notes.
			switch {
			case noIssue:
				none := ""
				e.Issue = &none
			case cmd.Flags().Changed("issue"):
				e.Issue = &issue
			}
			switch {
			case noNotes:
				none := ""
				e.Notes = &none
			case cmd.Flags().Changed("notes"):
				e.Notes = &notes
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
	cmd.Flags().StringVar(&issue, "issue", "", "new GitHub issue: a URL or owner/repo#N")
	cmd.Flags().BoolVar(&noIssue, "no-issue", false, "remove the issue link")
	cmd.Flags().StringVar(&notes, "notes", "", "new notes, replacing the old")
	cmd.Flags().BoolVar(&noNotes, "no-notes", false, "remove the notes")
	cmd.Flags().Int64Var(&project, "project", 0, "move the task to this project")
	cmd.MarkFlagsMutuallyExclusive("issue", "no-issue")
	cmd.MarkFlagsMutuallyExclusive("notes", "no-notes")
	return cmd
}

func taskCopyCmd(dbPath *string) *cobra.Command {
	var title, due, issue, notes string
	var project int64
	var noDue, noNotes, asJSON bool
	cmd := &cobra.Command{
		Use:   "copy ID",
		Short: "Make a new task from an existing one",
		Long: `Copy a task: the new task takes the original's title, due date, notes and
project unless a flag says otherwise, starts as todo, and gets the original's
subtasks unticked. The original's issue link is not copied; --issue gives the
copy one.`,
		Example: `  tasktracker task copy 3 --title "paint the landing" --due 2026-11-01`,
		Args:    cobra.ExactArgs(1),
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
			if cmd.Flags().Changed("issue") {
				e.Issue = &issue
			}
			// Cobra rejects --notes with --no-notes.
			switch {
			case noNotes:
				none := ""
				e.Notes = &none
			case cmd.Flags().Changed("notes"):
				e.Notes = &notes
			}
			if cmd.Flags().Changed("project") {
				e.ProjectID = &project
			}
			store, err := openStore(dbPath)
			if err != nil {
				return err
			}
			defer store.Close()
			n, err := store.CopyTask(cmd.Context(), id, e)
			if err != nil {
				return fmt.Errorf("task %d: %w", id, err)
			}
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), n)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "copied task %d to %d: %s (project %d, %d subtasks)\n", id, n.Task.ID, n.Task.Title, n.Task.ProjectID, len(n.Subtasks))
			return nil
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "title for the copy; the original's if not given")
	cmd.Flags().StringVar(&due, "due", "", "due date for the copy: YYYY-MM-DD, today or tomorrow")
	cmd.Flags().BoolVar(&noDue, "no-due", false, "give the copy no due date")
	cmd.Flags().StringVar(&issue, "issue", "", "GitHub issue for the copy: a URL or owner/repo#N")
	cmd.Flags().StringVar(&notes, "notes", "", "notes for the copy; the original's if not given")
	cmd.Flags().BoolVar(&noNotes, "no-notes", false, "give the copy no notes")
	cmd.Flags().Int64Var(&project, "project", 0, "put the copy in this project")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the copy with its subtasks as JSON")
	cmd.MarkFlagsMutuallyExclusive("notes", "no-notes")
	return cmd
}

func taskMarkCmd(dbPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "mark ID todo|done|dropped",
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

// taskArchiveCmd is archive or unarchive. Archiving hides a finished task
// from the tree; only a done or dropped task can be archived.
func taskArchiveCmd(dbPath *string, archive bool) *cobra.Command {
	use, short := "archive ID", "Put a finished task away, out of the tree"
	if !archive {
		use, short = "unarchive ID", "Bring an archived task back into the tree"
	}
	return &cobra.Command{
		Use:   use,
		Short: short,
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
			if err := store.ArchiveTask(cmd.Context(), id, archive); err != nil {
				return fmt.Errorf("task %d: %w", id, err)
			}
			if archive {
				fmt.Fprintf(cmd.OutOrStdout(), "task %d archived\n", id)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "task %d unarchived\n", id)
			}
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
