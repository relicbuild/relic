package transfer

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"relic/internal/platform/terminal"
)

type credentials struct {
	host                  string
	user, password, token string
	next                  http.RoundTripper
}

func (c credentials) RoundTrip(r *http.Request) (*http.Response, error) {
	if !strings.EqualFold(r.URL.Host, c.host) {
		return c.next.RoundTrip(r)
	}

	r = r.Clone(r.Context())

	switch {
	case c.token != "":
		r.Header.Set("Authorization", "Bearer "+c.token)
	case c.user != "":
		r.SetBasicAuth(c.user, c.password)
	}

	return c.next.RoundTrip(r)
}

func client(c credentials, certificateAuthority []byte) (*http.Client, error) {
	c.next = http.DefaultTransport

	if certificateAuthority != nil {
		roots, err := x509.SystemCertPool()
		if err != nil {
			roots = x509.NewCertPool()
		}

		if !roots.AppendCertsFromPEM(certificateAuthority) {
			return nil, errors.New("--ca-file holds no PEM certificate")
		}

		transport, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			return nil, errors.New("the default HTTP transport is not an *http.Transport")
		}

		transport = transport.Clone()
		transport.TLSClientConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    roots,
		}
		c.next = transport
	}

	return &http.Client{
		Transport: c,
	}, nil
}

// Command is the import command of the relic CLI.
func Command(destination func() (Server, error), status func(step string)) *cobra.Command {
	var (
		user, certificateAuthorityFile string
		passwordStdin, tokenStdin      bool
		opts                           Options
		output                         string
		verbose                        bool
	)

	cmd := &cobra.Command{
		Use:   "artifactory <url>",
		Short: "Import a JFrog Artifactory: groups, repositories, permission targets, files and their properties",
		Long: "Reads the Artifactory at <url> with its REST API (nothing is written to it) and copies into Relic " +
			"its groups, repositories (local, remote and virtual), permission targets, and the files of its " +
			"local repositories with their properties and checksums. Users are not copied: their " +
			"passwords cannot be read. Run it again to resume: what Relic already has is skipped (--update " +
			"replaces configuration). Signs in to the source with --user and a password, or a token, read from " +
			"standard input.",
		Example: "  relic import artifactory https://artifactory.example.com --user admin --password-stdin --dry-run\n" +
			"  cat token | relic import artifactory https://artifactory.example.com --token-stdin --repository libs-release",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if output != "text" && output != "json" {
				return fmt.Errorf("unknown output %q: text or json", output)
			}

			relic, err := destination()
			if err != nil {
				return err
			}

			sourceURL, err := url.Parse(normalize(args[0]))
			if err != nil {
				return fmt.Errorf("source URL: %w", err)
			}

			src := credentials{
				host: sourceURL.Host,
				user: user,
			}

			if passwordStdin || tokenStdin {
				line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
				if err != nil && !errors.Is(err, io.EOF) {
					return err
				}

				secret := strings.TrimRight(line, "\r\n")
				if secret == "" {
					return errors.New("standard input holds no password or token")
				}

				if tokenStdin {
					src.token = secret
				} else {
					src.password = secret
				}
			}

			if src.token == "" && src.user == "" {
				return errors.New("sign in to the source with --user and --password-stdin, or with --token-stdin")
			}

			var certificateAuthority []byte

			if certificateAuthorityFile != "" {
				//nolint:gosec // G304: the certificate authority file the user names
				if certificateAuthority, err = os.ReadFile(certificateAuthorityFile); err != nil {
					return fmt.Errorf("--ca-file: %w", err)
				}
			}

			sourceClient, err := client(src, certificateAuthority)
			if err != nil {
				return err
			}

			opts.Progress = func(format string, a ...any) {
				if verbose {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), format+"\n", a...)
				}

				status(fmt.Sprintf(format, a...))
			}

			report, err := Import(
				cmd.Context(),
				Server{
					URL:    normalize(args[0]),
					Client: sourceClient,
				},
				relic,
				opts,
			)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()

			if output == "json" {
				b, err := json.MarshalIndent(report, "", "  ")
				if err != nil {
					return err
				}

				_, _ = fmt.Fprintln(out, string(b))
			} else {
				if err := report.table(opts.DryRun).Print(out); err != nil {
					return err
				}

				for _, n := range report.Notes {
					_, _ = fmt.Fprintf(out, "note: %s\n", n)
				}

				for _, e := range report.Errors {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "error: %s\n", e)
				}
			}

			if len(report.Errors) > 0 {
				return fmt.Errorf("%d things could not be copied; run again to retry them", len(report.Errors))
			}

			return nil
		},
	}
	cmd.Flags().StringVar(&user, "user", "", "the source user")
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read the source user's password from standard input")
	cmd.Flags().BoolVar(&tokenStdin, "token-stdin", false, "read a source access token from standard input")
	cmd.Flags().StringVar(
		&certificateAuthorityFile,
		"ca-file",
		"",
		"a PEM certificate authority to trust for the source, besides the system's",
	)
	cmd.Flags().StringSliceVar(&opts.Repositories, "repository", nil, "import only these repositories (repeatable)")
	cmd.Flags().BoolVar(&opts.Update, "update", false, "replace the configuration Relic already has")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "report what would be copied, copy nothing")
	cmd.Flags().IntVar(&opts.Workers, "workers", 4, "files copied at once")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "report each thing copied on standard error")
	cmd.Flags().StringVarP(&output, "output", "o", "text", "output format: text or json")
	cmd.MarkFlagsMutuallyExclusive("password-stdin", "token-stdin")

	return cmd
}

func (r Report) table(isDryRun bool) terminal.Table {
	created, updated := "CREATED", "UPDATED"
	if isDryRun {
		created, updated = "WOULD CREATE", "WOULD UPDATE"
	}

	rows := make([][]string, 0, 4)

	for _, kind := range []struct {
		name   string
		counts Counts
	}{
		{
			name:   "groups",
			counts: r.Groups,
		},
		{
			name:   "repositories",
			counts: r.Repositories,
		},
		{
			name:   "permission targets",
			counts: r.Permissions,
		},
	} {
		rows = append(rows, []string{
			kind.name,
			strconv.Itoa(kind.counts.Created),
			strconv.Itoa(kind.counts.Updated),
			strconv.Itoa(kind.counts.Skipped),
			"",
		})
	}

	return terminal.Table{
		Headers: []string{
			"KIND",
			created,
			updated,
			"ALREADY THERE",
			"SIZE",
		},
		RightAligned: []int{
			1,
			2,
			3,
			4,
		},
		Rows: append(rows, []string{
			"files",
			strconv.Itoa(r.Files.Copied),
			"",
			strconv.Itoa(r.Files.Skipped),
			terminal.ByteSize(r.Files.Bytes),
		}),
	}
}
