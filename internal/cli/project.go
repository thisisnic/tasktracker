package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/thisisnic/tasktracker/internal/goallink"
	"github.com/thisisnic/tasktracker/internal/task"
)

func projectCmd(dbPath, cfgPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Add, list and update projects",
	}
	cmd.AddCommand(
		projectAddCmd(dbPath),
		projectListCmd(dbPath),
		projectShowCmd(dbPath, cfgPath),
		projectEditCmd(dbPath),
		projectMarkCmd(dbPath),
		projectDeleteCmd(dbPath),
	)
	return cmd
}

func projectAddCmd(dbPath *string) *cobra.Command {
	var in task.NewProject
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "add NAME",
		Short: "Add a project",
		Long: `Add a project. Give --goal once per goaltracker goal the project serves;
the ids are goaltracker's own.`,
		Example: `  tasktracker project add "house" --description "fix it up" --goal 3 --goal 7`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openStore(dbPath)
			if err != nil {
				return err
			}
			defer store.Close()
			in.Name = args[0]
			p, err := store.AddProject(cmd.Context(), in)
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), p)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "added project %d: %s\n", p.ID, p.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&in.Description, "description", "", "what the project is")
	cmd.Flags().Int64SliceVar(&in.GoalIDs, "goal", nil, "goaltracker goal id this project serves (repeatable)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the project as JSON")
	return cmd
}

func projectListCmd(dbPath *string) *cobra.Command {
	var f task.ProjectFilter
	var state string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List projects",
		Long:  `List active projects. --all includes done and shelved ones; --state lists one state only.`,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if state != "" {
				st, err := task.ParseState(state)
				if err != nil {
					return err
				}
				f.State = st
			}
			store, err := openStore(dbPath)
			if err != nil {
				return err
			}
			defer store.Close()
			projects, err := store.ListProjects(cmd.Context(), f)
			if err != nil {
				return err
			}
			if asJSON {
				if projects == nil {
					projects = []task.Project{}
				}
				return writeJSON(cmd.OutOrStdout(), projects)
			}
			if len(projects) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no projects. add one with: tasktracker project add \"...\"")
				return nil
			}
			open, err := openCounts(cmd, store)
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tPROJECT\tSTATE\tOPEN\tGOALS")
			for _, p := range projects {
				fmt.Fprintf(tw, "%d\t%s\t%s\t%d\t%s\n", p.ID, p.Name, p.State, open[p.ID], goalIDsText(p.GoalIDs))
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&f.All, "all", false, "include done and shelved projects")
	cmd.Flags().StringVar(&state, "state", "", "only projects in this state: active, done or shelved")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print as JSON")
	return cmd
}

// openCounts is the number of open tasks per project id.
func openCounts(cmd *cobra.Command, store *task.Store) (map[int64]int, error) {
	tasks, err := store.ListTasks(cmd.Context(), task.TaskFilter{Open: true})
	if err != nil {
		return nil, err
	}
	out := map[int64]int{}
	for _, t := range tasks {
		out[t.ProjectID]++
	}
	return out, nil
}

func goalIDsText(ids []int64) string {
	var parts []string
	for _, id := range ids {
		parts = append(parts, fmt.Sprintf("#%d", id))
	}
	return strings.Join(parts, " ")
}

type projectDetail struct {
	task.Project
	Goals []goallink.Goal `json:"goals"`
	Tasks []task.Task     `json:"tasks"`
}

func projectShowCmd(dbPath, cfgPath *string) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "show ID",
		Short: "Show a project with its goals and tasks",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseID("project", args[0])
			if err != nil {
				return err
			}
			store, err := openStore(dbPath)
			if err != nil {
				return err
			}
			defer store.Close()
			p, err := store.GetProject(cmd.Context(), id)
			if err != nil {
				return fmt.Errorf("project %d: %w", id, err)
			}
			tasks, err := store.ListTasks(cmd.Context(), task.TaskFilter{ProjectID: id})
			if err != nil {
				return err
			}
			// Goal statements are a nicety: without goaltracker's database
			// the ids are shown bare and the reason goes to stderr.
			goals, goalErr := goalReader(*cfgPath).Lookup(cmd.Context(), p.GoalIDs)
			if goalErr != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "note: goal statements not shown: %v\n", goalErr)
			}
			if asJSON {
				if tasks == nil {
					tasks = []task.Task{}
				}
				return writeJSON(cmd.OutOrStdout(), projectDetail{Project: p, Goals: goals, Tasks: tasks})
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "#%d  %s\n", p.ID, p.Name)
			fmt.Fprintf(out, "state:  %s\n", p.State)
			if p.Description != "" {
				fmt.Fprintf(out, "about:  %s\n", p.Description)
			}
			if len(goals) > 0 {
				fmt.Fprintln(out, "goals:")
				for _, g := range goals {
					fmt.Fprintf(out, "  %s\n", g.Label())
				}
			}
			if len(tasks) > 0 {
				fmt.Fprintln(out, "tasks:")
				tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
				for _, t := range tasks {
					fmt.Fprintf(tw, "  %d\t%s\t%s\t%s\n", t.ID, t.Status, t.Due, t.Title)
				}
				tw.Flush()
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print as JSON")
	return cmd
}

func projectEditCmd(dbPath *string) *cobra.Command {
	var name, description string
	var goals []int64
	var noGoals bool
	cmd := &cobra.Command{
		Use:   "edit ID",
		Short: "Change a project's name, description or goals",
		Long: `Change a project. --goal replaces the whole set of goal links, so give it
once per goal to keep; --no-goals removes them all.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseID("project", args[0])
			if err != nil {
				return err
			}
			var e task.ProjectEdit
			if cmd.Flags().Changed("name") {
				e.Name = &name
			}
			if cmd.Flags().Changed("description") {
				e.Description = &description
			}
			switch {
			case noGoals && cmd.Flags().Changed("goal"):
				return errors.New("--goal and --no-goals cannot both be given")
			case noGoals:
				none := []int64{}
				e.GoalIDs = &none
			case cmd.Flags().Changed("goal"):
				e.GoalIDs = &goals
			}
			if e.Name == nil && e.Description == nil && e.GoalIDs == nil {
				return errors.New("nothing to change; give at least one flag")
			}
			store, err := openStore(dbPath)
			if err != nil {
				return err
			}
			defer store.Close()
			p, err := store.UpdateProject(cmd.Context(), id, e)
			if err != nil {
				return fmt.Errorf("project %d: %w", id, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "updated project %d: %s\n", p.ID, p.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "new name")
	cmd.Flags().StringVar(&description, "description", "", "new description; empty clears it")
	cmd.Flags().Int64SliceVar(&goals, "goal", nil, "goaltracker goal id to link (repeatable; replaces the set)")
	cmd.Flags().BoolVar(&noGoals, "no-goals", false, "remove every goal link")
	return cmd
}

func projectMarkCmd(dbPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "mark ID active|done|shelved",
		Short: "Set a project's state",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseID("project", args[0])
			if err != nil {
				return err
			}
			st, err := task.ParseState(args[1])
			if err != nil {
				return err
			}
			store, err := openStore(dbPath)
			if err != nil {
				return err
			}
			defer store.Close()
			if err := store.MarkProject(cmd.Context(), id, st); err != nil {
				return fmt.Errorf("project %d: %w", id, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "project %d marked %s\n", id, st)
			return nil
		},
	}
}

func projectDeleteCmd(dbPath *string) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "delete ID",
		Short: "Delete a project with all its tasks and subtasks",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseID("project", args[0])
			if err != nil {
				return err
			}
			store, err := openStore(dbPath)
			if err != nil {
				return err
			}
			defer store.Close()
			p, err := store.GetProject(cmd.Context(), id)
			if err != nil {
				return fmt.Errorf("project %d: %w", id, err)
			}
			if !yes && !confirm(cmd, fmt.Sprintf("delete project %d %q and every task in it?", p.ID, p.Name)) {
				fmt.Fprintln(cmd.OutOrStdout(), "kept")
				return nil
			}
			if err := store.DeleteProject(cmd.Context(), id); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "deleted project %d\n", id)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip the confirmation prompt")
	return cmd
}

// printTree writes projects, tasks and subtasks as an indented table.
func printTree(w io.Writer, tree []task.ProjectNode) {
	if len(tree) == 0 {
		fmt.Fprintln(w, "no projects. add one with: tasktracker project add \"...\"")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTATUS\tDUE\tTITLE")
	for _, p := range tree {
		fmt.Fprintf(tw, "P%d\t%s\t\t%s\n", p.Project.ID, p.Project.State, p.Project.Name)
		for _, t := range p.Tasks {
			fmt.Fprintf(tw, "%d\t%s\t%s\t  %s\n", t.Task.ID, t.Task.Status, t.Task.Due, t.Task.Title)
			for _, s := range t.Subtasks {
				box := "[ ]"
				if s.Done {
					box = "[x]"
				}
				fmt.Fprintf(tw, "S%d\t%s\t\t    %s\n", s.ID, box, s.Title)
			}
		}
	}
	tw.Flush()
}
