package main

import (
	"errors"
	"fmt"
	"strconv"

	"relic/internal/platform/database"

	"github.com/spf13/cobra"

	"relic/internal/platform/terminal"
)

func databaseCmd(getenv func(string) string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "database",
		Short: "Copy a Relic database between PostgreSQL and SQLite, or back up SQLite",
	}
	cmd.AddCommand(
		databaseCopyCmd(),
		databaseBackupCmd(getenv),
	)

	return cmd
}

func databaseCopyCmd() *cobra.Command {
	var from, to string

	cmd := &cobra.Command{
		Use:   "copy --from <database URL> --to <database URL>",
		Short: "Copy every table into an empty database (PostgreSQL to SQLite, or back)",
		Long: `Copy migrates the target, copies every table into it in one transaction,
and compares the row counts. The target must be empty, and Relic must not
write to the source meanwhile: stop the server and the worker first. The
content in object storage stays where it is; point the new installation at
the same storage. Background jobs are not copied: the worker schedules its
periodic jobs again.

URLs: postgres://<user>:<password>@<host>/<database>, or
sqlite:///absolute/path/relic.db.`,
		Example: `  relic server database copy --from "$RELIC_DATABASE_URL" --to sqlite:///var/lib/relic/relic.db`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()

			src, err := database.Open(ctx, from)
			if err != nil {
				return fmt.Errorf("open the source: %w", err)
			}
			defer src.Close()

			dst, err := database.Open(ctx, to)
			if err != nil {
				return fmt.Errorf("open the target: %w", err)
			}
			defer dst.Close()

			var rows int64

			copied := terminal.Table{
				Headers: []string{
					"TABLE",
					"ROWS",
				},
				RightAligned: []int{
					1,
				},
			}

			err = database.Copy(ctx, src, dst, func(table string, n int64) {
				rows += n

				copied.Rows = append(copied.Rows, []string{
					table,
					strconv.FormatInt(n, 10),
				})
			})
			if err != nil {
				return printCompleted(cmd.OutOrStdout(), copied, err)
			}

			if err := copied.Print(cmd.OutOrStdout()); err != nil {
				return err
			}

			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "copied %d tables, %d rows\n", len(copied.Rows), rows)

			return nil
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "the database to copy (required)")
	cmd.Flags().StringVar(&to, "to", "", "the empty database to copy into (required)")
	_ = cmd.MarkFlagRequired("from")
	_ = cmd.MarkFlagRequired("to")

	return cmd
}

func databaseBackupCmd(getenv func(string) string) *cobra.Command {
	var url string

	cmd := &cobra.Command{
		Use:   "backup <file>",
		Short: "Write a consistent snapshot of a SQLite database while Relic runs",
		Long: `Backup writes a snapshot of the SQLite database to a new file while the
server and the worker keep running. Restore it by stopping Relic and putting
the file in the database's place. PostgreSQL databases are backed up with
pg_dump.`,
		Example: `  relic server database backup /backups/relic-$(date +%F).db`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if url == "" {
				url = getenv("RELIC_DATABASE_URL")
			}

			if url == "" {
				return errors.New("set RELIC_DATABASE_URL or --database-url")
			}

			db, err := database.Open(cmd.Context(), url)
			if err != nil {
				return err
			}
			defer db.Close()

			if err := database.Backup(cmd.Context(), db, args[0]); err != nil {
				return err
			}

			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "backed up to %s\n", args[0])

			return nil
		},
	}
	cmd.Flags().StringVar(&url, "database-url", "", "the database (default $RELIC_DATABASE_URL)")

	return cmd
}
