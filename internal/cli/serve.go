package cli

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/thisisnic/tasktracker/internal/config"
	"github.com/thisisnic/tasktracker/internal/server"
	"github.com/thisisnic/tasktracker/internal/version"
)

// runServe is what the bare command does: serve the browser UI and its
// API on a loopback port until interrupted, opening a browser tab first
// unless told not to. port is the --port flag, 0 when not given, in
// which case the config's port is used.
func runServe(cmd *cobra.Command, dbPath, cfgPath string, port int, noOpen bool) error {
	// The config names the port and the goaltracker database; a broken
	// one is reported, and the defaults stand, as the TUI and the goal
	// lookup do, so a typo in the backup section does not keep the UI
	// from opening. A port out of range is reported the same way and
	// the default used, rather than port 0 binding a random one that
	// the printed URL does not name.
	cfg, cfgErr := config.Load(cfgPath)
	flagged := cmd.Flags().Changed("port")
	switch err := cfg.PortError(); {
	case cfgErr != nil && flagged:
		fmt.Fprintf(cmd.ErrOrStderr(), "note: config: %v; using goaltracker's default database\n", cfgErr)
	case cfgErr != nil:
		fmt.Fprintf(cmd.ErrOrStderr(), "note: config: %v; using the default port and goaltracker's default database\n", cfgErr)
	case err != nil && !flagged:
		// With --port the config's port is not used, so a bad one is
		// nothing to say.
		fmt.Fprintf(cmd.ErrOrStderr(), "note: config: %s: %v; using the default port\n", cfgPath, err)
		cfg.Port = config.DefaultPort
	}
	goals := goalsFor(cfg, cfgErr)
	if !flagged {
		port = cfg.Port
	} else if err := (config.Config{Port: port}).PortError(); err != nil {
		// The flag is a mistake to fix, not a file to carry on past.
		return fmt.Errorf("--%v", err)
	}
	store, err := openStore(&dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	ln, err := server.Listen(port)
	if err != nil {
		return err
	}
	srv := server.New(server.Options{Store: store, Goals: goals, Version: version.String()})
	url := fmt.Sprintf("http://127.0.0.1:%d/", port)
	fmt.Fprintf(cmd.OutOrStdout(), "tasktracker %s serving %s (database %s)\n", version.String(), url, dbPath)
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if !noOpen {
		if err := server.OpenBrowser(url); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "could not open a browser (%v); open %s yourself\n", err, url)
		}
	}
	return server.Serve(ctx, ln, srv)
}
