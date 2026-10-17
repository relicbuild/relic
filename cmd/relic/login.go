package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"
	"uuid"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	relicv1 "relic/api/gen/relic/v1"
)

// webLoginPoll is how often a browser sign-in asks whether it was approved.
const webLoginPoll = time.Second

func loginCmd(g *globals) *cobra.Command {
	var (
		user, name, oidcToken, identity string
		certificateAuthorityFile        string
		passwordStdin                   bool
		expiresDays                     int32
	)

	cmd := &cobra.Command{
		Use: "login <url>",
		Annotations: map[string]string{
			progressAnnotation: "off",
		},
		Short: "Sign in and save an access token as a context",
		Long: "Signs in, creates an access token for the CLI and saves it, with the server URL, as a context\n" +
			"in the config file (mode 0600). Without --user, you approve the sign-in in the browser, where\n" +
			"single sign-on works. With --user, the password is read from the terminal, or from standard\n" +
			"input with --password-stdin; it is never stored. With --oidc-token, a CI job exchanges its\n" +
			"OIDC token through a workload identity for a token that lives up to an hour.\n\n" +
			"When the server's certificate comes from an authority this machine does not trust, such as the\n" +
			"one a Relic server generates, --ca-file names that authority's certificate; on a terminal, the\n" +
			"CLI shows its SHA-256 fingerprint and asks instead. The context keeps the authority.",
		Example: "  relic auth login https://relic.example.com\n" +
			"  relic auth login https://relic.example.com --user alice\n" +
			"  echo \"$PASSWORD\" | relic auth login https://relic.example.com --user ci --password-stdin --context ci\n" +
			"  relic auth login https://relic.example.com --identity github --oidc-token \"$ID_TOKEN\"\n" +
			"  relic auth login https://relic.example.com --user alice --ca-file ca.crt",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			base := strings.TrimRight(args[0], "/")

			u, err := url.Parse(base)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
				return fmt.Errorf("%q is not an http(s) URL without credentials", args[0])
			}

			canAsk := !passwordStdin && oidcToken != "-" && stdinIsTerminal(cmd)

			certificateAuthority, err := trustedAuthority(cmd, u, certificateAuthorityFile, canAsk)
			if err != nil {
				return err
			}

			server := trustedServer{
				url:                  base,
				certificateAuthority: certificateAuthority,
			}

			var token, who string

			switch {
			case oidcToken != "":
				token, who, err = exchangeWorkloadToken(cmd, server, identity, oidcToken)
			case user != "":
				token, err = passwordLogin(cmd, server, user, passwordStdin, expiresDays)
				who = user
			default:
				token, who, err = browserLogin(cmd, server, expiresDays)
			}

			if err != nil {
				return err
			}

			if name == "" {
				name = u.Host
			}

			if err := g.updateConfig(func(c *cliConfig) error {
				c.set(cliContext{
					Name:                 name,
					URL:                  base,
					User:                 who,
					Token:                token,
					CertificateAuthority: certificateAuthority,
				})

				return nil
			}); err != nil {
				return err
			}

			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Signed in to %s as %s (context %s).\n", base, who, name)

			return nil
		},
	}
	cmd.Flags().StringVarP(&user, "user", "u", "", "user name, to sign in with a password")
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read the password from standard input")
	cmd.Flags().StringVar(&name, "context", "", "context name (default: the server's host)")
	cmd.Flags().
		Int32Var(&expiresDays, "expires-days", 90, "token lifetime in days; 0 never expires for administrators and is 365 for other users")
	cmd.Flags().
		StringVar(&oidcToken, "oidc-token", "", "a CI job's OIDC token to exchange; - reads it from standard input")
	cmd.Flags().StringVar(&identity, "identity", "", "the workload identity that trusts the OIDC token's issuer")
	cmd.Flags().StringVar(
		&certificateAuthorityFile,
		"ca-file",
		"",
		"a PEM certificate authority to trust for this server (a Relic server's ca.crt)",
	)
	cmd.MarkFlagsRequiredTogether("oidc-token", "identity")
	cmd.MarkFlagsMutuallyExclusive("user", "oidc-token")

	return cmd
}

type trustedServer struct {
	url                  string
	certificateAuthority string
}

func (s trustedServer) client(creds credentials) *client {
	return newClient(s.url, s.certificateAuthority, creds)
}

func passwordLogin(
	cmd *cobra.Command,
	server trustedServer,
	user string,
	fromStdin bool,
	expiresDays int32,
) (string, error) {
	password, err := readPassword(cmd, fromStdin)
	if err != nil {
		return "", err
	}

	return cliToken(cmd, server.client(credentials{
		user:     user,
		password: password,
	}), expiresDays)
}

func cliToken(cmd *cobra.Command, c *client, expiresDays int32) (string, error) {
	resp, err := c.security.CreateToken(cmd.Context(), connect.NewRequest(&relicv1.CreateTokenRequest{
		Description: "relic CLI",
		ExpiresDays: expiresDays,
	}))
	if err != nil {
		return "", fmt.Errorf("sign in: %w", err)
	}

	return resp.Msg.GetAccessToken(), nil
}

// browserLogin has a signed-in user approve the sign-in in the browser,
// then trades the short token it gets for the CLI's own.
func browserLogin(cmd *cobra.Command, server trustedServer, expiresDays int32) (string, string, error) {
	anon := server.client(credentials{})
	session := uuid.New().String()

	started, err := anon.sessions.StartWebLogin(cmd.Context(), connect.NewRequest(&relicv1.StartWebLoginRequest{
		Session: session,
	}))
	if err != nil {
		return "", "", fmt.Errorf("start the sign-in: %w", err)
	}

	_, _ = fmt.Fprintf(
		cmd.ErrOrStderr(),
		"Open %s%s in your browser, sign in, and approve the code %s.\n",
		server.url,
		started.Msg.GetApprovePath(),
		started.Msg.GetCode(),
	)

	ticker := time.NewTicker(webLoginPoll)
	defer ticker.Stop()

	for {
		polled, err := anon.sessions.PollWebLogin(cmd.Context(), connect.NewRequest(&relicv1.PollWebLoginRequest{
			Session:     session,
			Description: "relic CLI sign-in",
		}))
		if err != nil {
			return "", "", fmt.Errorf("wait for the approval: %w", err)
		}

		if polled.Msg.GetApproved() {
			c := server.client(credentials{
				token: polled.Msg.GetAccessToken(),
			})

			me, err := c.sessions.GetCurrentUser(cmd.Context(), connect.NewRequest(&relicv1.GetCurrentUserRequest{}))
			if err != nil {
				return "", "", fmt.Errorf("sign in: %w", err)
			}

			token, err := cliToken(cmd, c, expiresDays)

			return token, me.Msg.GetPrincipal().GetUsername(), err
		}

		select {
		case <-cmd.Context().Done():
			return "", "", cmd.Context().Err()
		case <-ticker.C:
		}
	}
}

func exchangeWorkloadToken(
	cmd *cobra.Command,
	server trustedServer,
	identity, oidcToken string,
) (string, string, error) {
	if oidcToken == "-" {
		line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", "", fmt.Errorf("read the OIDC token: %w", err)
		}

		oidcToken = strings.TrimSpace(line)
		if oidcToken == "" {
			return "", "", errors.New("no OIDC token on standard input")
		}
	}

	resp, err := server.client(credentials{}).sessions.ExchangeWorkloadToken(
		cmd.Context(),
		connect.NewRequest(&relicv1.ExchangeWorkloadTokenRequest{
			Identity: identity,
			Token:    oidcToken,
		}),
	)
	if err != nil {
		return "", "", fmt.Errorf("exchange the OIDC token: %w", err)
	}

	return resp.Msg.GetAccessToken(), resp.Msg.GetUsername(), nil
}

func readPassword(cmd *cobra.Command, fromStdin bool) (string, error) {
	in := cmd.InOrStdin()
	if fromStdin {
		line, err := bufio.NewReader(in).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("read the password: %w", err)
		}

		if p := strings.TrimRight(line, "\r\n"); p != "" {
			return p, nil
		}

		return "", errors.New("no password on standard input")
	}

	f, ok := in.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return "", errors.New("standard input is not a terminal: use --password-stdin")
	}

	_, _ = fmt.Fprint(cmd.ErrOrStderr(), "Password: ")
	b, err := term.ReadPassword(int(f.Fd()))
	_, _ = fmt.Fprintln(cmd.ErrOrStderr())

	if err != nil {
		return "", fmt.Errorf("read the password: %w", err)
	}

	return string(b), nil
}
