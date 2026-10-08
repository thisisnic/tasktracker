package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/thisisnic/tasktracker/internal/backup"
	"github.com/thisisnic/tasktracker/internal/config"
	"github.com/thisisnic/tasktracker/internal/task"
)

func keyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "key",
		Short: "Manage the backup encryption key",
	}
	cmd.AddCommand(keyNewCmd())
	return cmd
}

func keyNewCmd() *cobra.Command {
	var out string
	cmd := &cobra.Command{
		Use:   "new",
		Short: "Create an age keypair for encrypting backups",
		Long: `Create an age keypair. The private key is written to a file only you can
read. The public key is printed with a config file you can copy into place.

Keep a copy of the private key somewhere safe that is not this machine, such
as a password manager. Backups cannot be opened without it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if out == "" {
				out = config.DefaultIdentityFile()
			}
			recipient, err := backup.NewKey(out)
			if err != nil {
				if errors.Is(err, os.ErrExist) {
					return fmt.Errorf("%s already exists; pass --out to write somewhere else", out)
				}
				return err
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "private key written to %s\n", out)
			fmt.Fprintf(w, "public key: %s\n\n", recipient)
			fmt.Fprintf(w, "Copy the private key file's contents into your password manager now.\n\n")
			fmt.Fprintf(w, "Then put this in %s:\n\n%s", config.Path(), config.Example(recipient, out))
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "where to write the private key (default "+config.DefaultIdentityFile()+")")
	return cmd
}

func backupCmd(dbPath, cfgPath *string) *cobra.Command {
	var dir, recipient string
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Write an encrypted copy of the database to the backup folder",
		Long: `Write a consistent, encrypted copy of the database as tasktracker.db.age in the
backup folder, replacing the previous one. Nothing is written if the
database is unchanged since the last backup. The folder and key come from
the config file unless given here.

The serving process does this itself: once when it starts and then every
12 hours, as long as a backup folder and key are configured.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(*cfgPath)
			if err != nil {
				return err
			}
			if dir != "" {
				cfg.Backup.Dir = config.ExpandHome(dir)
			}
			if recipient != "" {
				cfg.Backup.Recipient = recipient
			}
			if !cfg.Backup.Configured() {
				return fmt.Errorf("no backup folder or key configured; run `tasktracker key new` and follow its instructions, or pass --dir and --recipient")
			}
			store, err := openStore(dbPath)
			if err != nil {
				return err
			}
			defer store.Close()
			return runBackup(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), store, *dbPath, cfg.Backup)
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "backup folder (overrides config)")
	cmd.Flags().StringVar(&recipient, "recipient", "", "age public key (overrides config)")
	return cmd
}

// autoBackupEvery is how often the serving process backs up the database,
// in wall-clock time. autoBackupPoll is how often it checks whether that
// much has passed: a ticker alone would not do, because Go's timers run on
// the monotonic clock, which stops while a laptop is asleep, so a 12-hour
// ticker would count only hours awake. autoBackupNow is the wall clock
// it compares. All three are variables so tests can shorten the
// intervals and move the clock.
var (
	autoBackupEvery = 12 * time.Hour
	autoBackupPoll  = time.Hour
	autoBackupNow   = time.Now
)

// autoBackup backs up the database when serving starts and then whenever
// autoBackupEvery has passed since the last run, until ctx ends.
// backup.Run skips an unchanged database, so a quiet run writes nothing
// and only a change costs a snapshot. A failed backup is reported on errw
// and tried again when the interval next passes; it never stops the
// server. The run in flight when ctx ends is cut short and not reported
// as a failure: the caller is shutting down, not failing. (A commit that
// landed but could not be pushed is still mentioned, since the next run
// pushes it.)
func autoBackup(ctx context.Context, out, errw io.Writer, s *task.Store, dbPath string, b config.Backup) {
	tick := time.NewTicker(autoBackupPoll)
	defer tick.Stop()
	var last time.Time
	for {
		// Round(0) strips the monotonic reading, so the comparison below
		// is between wall-clock times and counts time spent asleep. A
		// clock set back since the last run starts the schedule over
		// rather than waiting for it to catch up.
		if now := autoBackupNow().Round(0); last.IsZero() || now.Before(last) || now.Sub(last) >= autoBackupEvery {
			last = now
			if err := runBackup(ctx, out, errw, s, dbPath, b); err != nil && ctx.Err() == nil {
				fmt.Fprintf(errw, "%v; trying again in %s\n", err, autoBackupEvery)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// runBackup takes a snapshot and reports what happened on out; a push
// that failed after its commit is a warning on errw, not an error.
func runBackup(ctx context.Context, out, errw io.Writer, store *task.Store, dbPath string, b config.Backup) error {
	res, err := backup.Run(ctx, store, backup.Options{
		Dir:       b.Dir,
		Recipient: b.Recipient,
		Marker:    backup.MarkerPath(config.Dir(), dbPath, b.Dir),
	})
	if err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	if res.Skipped {
		fmt.Fprintln(out, "backup: no changes since the last backup")
	} else {
		fmt.Fprintf(out, "backup: wrote %s\n", res.Path)
	}
	if !b.Git {
		return nil
	}
	return pushBackup(ctx, out, errw, b)
}

// pushBackup commits and pushes the backup file in the data repo, and
// says so on out when there was something to push. A push that fails
// is a warning on errw, not an error: the commit is safe locally, and
// every later run, including a quit that changed nothing, pushes it.
func pushBackup(ctx context.Context, out, errw io.Writer, b config.Backup) error {
	pushed, err := backup.Push(ctx, b.Dir, time.Now())
	switch {
	case err == nil:
		if pushed {
			fmt.Fprintln(out, "backup: pushed")
		}
		return nil
	case errors.Is(err, backup.ErrPushFailed):
		fmt.Fprintf(errw, "backup: committed locally but not pushed; will retry next time. %v\n", err)
		return nil
	default:
		return fmt.Errorf("backup git: %w", err)
	}
}

// hasBackup reports whether a backup has been written to dir, which is
// when there can be a commit waiting to be pushed.
func hasBackup(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, backup.FileName))
	return err == nil
}

func restoreCmd(dbPath, cfgPath *string) *cobra.Command {
	var identity string
	var yes bool
	cmd := &cobra.Command{
		Use:   "restore [FILE]",
		Short: "Replace the database with a decrypted backup",
		Long: `Decrypt a backup with your private key and put it in place of the current
database. With no FILE, the tasktracker.db.age in the configured backup folder is
used. The current database is kept next to it as tasktracker.db.bak. Close any
running tasktracker first.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(*cfgPath)
			if err != nil {
				return err
			}
			if identity == "" {
				identity = cfg.Backup.IdentityFile
			}
			if identity == "" {
				return fmt.Errorf("no private key: set identity_file in %s or pass --identity", *cfgPath)
			}
			var file string
			switch {
			case len(args) == 1:
				file = args[0]
			case cfg.Backup.Dir != "":
				file = filepath.Join(cfg.Backup.Dir, backup.FileName)
			default:
				return fmt.Errorf("no backup file given and no backup folder in %s", *cfgPath)
			}
			if !yes && !confirm(cmd, fmt.Sprintf("replace %s with %s? The current database is kept as .bak", *dbPath, file)) {
				fmt.Fprintln(cmd.OutOrStdout(), "kept")
				return nil
			}
			kept, err := backup.Restore(file, config.ExpandHome(identity), *dbPath, time.Now())
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "restored %s from %s\n", *dbPath, file)
			if kept != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "previous database kept at %s\n", kept)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&identity, "identity", "", "age private key file (overrides config)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip the confirmation prompt")
	return cmd
}
