package cli

import (
	"errors"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/thisisnic/tasktracker/internal/task"
)

func areaCmd(dbPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "area",
		Short: "Group projects into areas, which nest",
		Long: `An area groups projects, and other areas, above the project level. It is a
name with a place in a tree and nothing else: no state, no due date. Put a
project in one with project add --in or project edit --in.`,
	}
	cmd.AddCommand(
		areaAddCmd(dbPath),
		areaListCmd(dbPath),
		areaEditCmd(dbPath),
		areaDeleteCmd(dbPath),
	)
	return cmd
}

func areaAddCmd(dbPath *string) *cobra.Command {
	var in task.NewArea
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "add NAME",
		Short: "Add an area, at the top level or inside another",
		Example: `  tasktracker area add "home"
  tasktracker area add "garden" --in 1`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openStore(dbPath)
			if err != nil {
				return err
			}
			defer store.Close()
			in.Name = args[0]
			a, err := store.AddArea(cmd.Context(), in)
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), a)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "added area %d: %s\n", a.ID, a.Name)
			return nil
		},
	}
	cmd.Flags().Int64Var(&in.ParentID, "in", 0, "id of the area this one goes inside")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the area as JSON")
	return cmd
}

func areaListCmd(dbPath *string) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List areas as a tree",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openStore(dbPath)
			if err != nil {
				return err
			}
			defer store.Close()
			areas, err := store.ListAreas(cmd.Context())
			if err != nil {
				return err
			}
			if asJSON {
				if areas == nil {
					areas = []task.Area{}
				}
				return writeJSON(cmd.OutOrStdout(), areas)
			}
			if len(areas) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no areas. add one with: tasktracker area add \"...\"")
				return nil
			}
			projects, err := store.ListProjects(cmd.Context(), task.ProjectFilter{})
			if err != nil {
				return err
			}
			var nodes []task.ProjectNode
			for _, p := range projects {
				nodes = append(nodes, task.ProjectNode{Project: p})
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tAREA\tPROJECTS")
			var walk func(nodes []task.AreaNode, depth int)
			walk = func(nodes []task.AreaNode, depth int) {
				for _, n := range nodes {
					_, ps := n.Counts()
					fmt.Fprintf(tw, "%d\t%s%s\t%d\n", n.Area.ID, strings.Repeat("  ", depth), n.Area.Name, ps)
					walk(n.Areas, depth+1)
				}
			}
			walk(task.Nest(areas, nodes).Areas, 0)
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print as JSON")
	return cmd
}

func areaEditCmd(dbPath *string) *cobra.Command {
	var name string
	var in int64
	var top bool
	cmd := &cobra.Command{
		Use:   "edit ID",
		Short: "Rename an area or move it",
		Long:  `Rename an area with --name. Move it inside another area with --in, or to the top level with --top.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseID("area", args[0])
			if err != nil {
				return err
			}
			var e task.AreaEdit
			if cmd.Flags().Changed("name") {
				e.Name = &name
			}
			switch {
			case top && cmd.Flags().Changed("in"):
				return errors.New("--in and --top cannot both be given")
			case top:
				none := int64(0)
				e.ParentID = &none
			case cmd.Flags().Changed("in"):
				e.ParentID = &in
			}
			if e.Name == nil && e.ParentID == nil {
				return errors.New("nothing to change; give at least one flag")
			}
			store, err := openStore(dbPath)
			if err != nil {
				return err
			}
			defer store.Close()
			a, err := store.UpdateArea(cmd.Context(), id, e)
			if err != nil {
				return fmt.Errorf("area %d: %w", id, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "updated area %d: %s\n", a.ID, a.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "new name")
	cmd.Flags().Int64Var(&in, "in", 0, "id of the area to move this one inside")
	cmd.Flags().BoolVar(&top, "top", false, "move the area to the top level")
	return cmd
}

func areaDeleteCmd(dbPath *string) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "delete ID",
		Short: "Delete an area; what is in it moves up a level",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseID("area", args[0])
			if err != nil {
				return err
			}
			store, err := openStore(dbPath)
			if err != nil {
				return err
			}
			defer store.Close()
			a, err := store.GetArea(cmd.Context(), id)
			if err != nil {
				return fmt.Errorf("area %d: %w", id, err)
			}
			if !yes && !confirm(cmd, fmt.Sprintf("delete area %d %q and move what is in it up a level?", a.ID, a.Name)) {
				fmt.Fprintln(cmd.OutOrStdout(), "kept")
				return nil
			}
			if err := store.DeleteArea(cmd.Context(), id); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "deleted area %d\n", id)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip the confirmation prompt")
	return cmd
}
