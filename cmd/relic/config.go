package main

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	neturl "net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"

	"relic/internal/platform/localdisk"
	"relic/internal/platform/servertls"

	"relic/internal/platform/terminal"
)

type cliConfig struct {
	Current  string       `yaml:"current,omitempty"`
	Contexts []cliContext `yaml:"contexts,omitempty"`
}

type cliContext struct {
	Name                 string `yaml:"name"`
	URL                  string `yaml:"url"`
	User                 string `yaml:"user,omitempty"`
	Token                string `yaml:"token"`
	CertificateAuthority string `yaml:"certificateAuthority,omitempty"`
}

const (
	defaultListenAddress        = ":8090"
	defaultMetricsListenAddress = ":8091"
)

func (g *globals) configPath() (string, error) {
	if g.config != "" {
		return g.config, nil
	}

	if p := g.getenv("RELIC_CONFIG"); p != "" {
		return p, nil
	}

	userConfigDir := os.UserConfigDir
	if g.userConfigDir != nil {
		userConfigDir = g.userConfigDir
	}

	dir, err := userConfigDir()
	if err != nil {
		return "", fmt.Errorf("find the config directory: %w", err)
	}

	return filepath.Join(dir, "relic", "config.yaml"), nil
}

func (g *globals) loadConfig() (cliConfig, string, error) {
	path, err := g.configPath()
	if err != nil {
		return cliConfig{}, "", err
	}

	var c cliConfig

	b, err := os.ReadFile(path) //nolint:gosec // the user's own config file
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return c, path, nil
	case err != nil:
		return c, path, fmt.Errorf("read config: %w", err)
	}

	if err := yaml.Unmarshal(b, &c); err != nil {
		return c, path, fmt.Errorf("parse %s: %w", path, err)
	}

	return c, path, nil
}

func saveConfig(path string, c cliConfig) error {
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create the config directory: %w", err)
	}

	if err := writeFileAtomic(path, bytes.NewReader(b), 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	return nil
}

func (g *globals) updateConfig(change func(*cliConfig) error) error {
	path, err := g.configPath()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create the config directory: %w", err)
	}

	unlock, err := localdisk.Lock(path + ".lock")
	if err != nil {
		return err
	}
	defer unlock()

	c, _, err := g.loadConfig()
	if err != nil {
		return err
	}

	if err := change(&c); err != nil {
		return err
	}

	return saveConfig(path, c)
}

func (c *cliConfig) set(ctx cliContext) {
	i := slices.IndexFunc(c.Contexts, func(x cliContext) bool {
		return x.Name == ctx.Name
	})
	if i < 0 {
		c.Contexts = append(c.Contexts, ctx)
	} else {
		c.Contexts[i] = ctx
	}

	c.Current = ctx.Name
}

func (c cliConfig) find(name string) (cliContext, bool) {
	i := slices.IndexFunc(c.Contexts, func(x cliContext) bool {
		return x.Name == name
	})
	if i < 0 {
		return cliContext{}, false
	}

	return c.Contexts[i], true
}

type serverTarget struct {
	url   string
	token string
	// contextToken reports whether token is the saved context's own, which only the CLI holds.
	contextToken         bool
	username             string
	password             string
	certificateAuthority string
}

func (t serverTarget) credentials() credentials {
	return credentials{
		token:    t.token,
		user:     t.username,
		password: t.password,
	}
}

func (g *globals) target() (serverTarget, error) {
	url, token := g.getenv("RELIC_URL"), strings.TrimSpace(g.getenv("RELIC_TOKEN"))
	if g.context != "" {
		url, token = "", ""
	}

	var (
		certificateAuthority string
		contextToken         bool
	)

	if g.url == "" || g.token == "" {
		c, _, err := g.loadConfig()
		if err != nil {
			return serverTarget{}, err
		}

		name := g.context
		if name == "" {
			name = c.Current
		}

		if ctx, ok := c.find(name); ok {
			chosen := cmp.Or(g.url, url)
			if chosen == "" || strings.TrimRight(chosen, "/") == strings.TrimRight(ctx.URL, "/") {
				url = ctx.URL
				certificateAuthority = ctx.CertificateAuthority

				if token == "" {
					token, contextToken = ctx.Token, true
				}
			}
		} else if g.context != "" {
			return serverTarget{}, fmt.Errorf("no context %q in the config file", g.context)
		}
	}

	if g.url != "" {
		url = g.url
	}

	if g.token != "" {
		token, contextToken = strings.TrimSpace(g.token), false
	}

	if strings.ContainsFunc(token, unicode.IsSpace) || strings.ContainsFunc(token, unicode.IsControl) {
		return serverTarget{}, errors.New("the access token holds spaces or control characters")
	}

	if url == "" {
		hasChosenConfig := g.config != "" || g.getenv("RELIC_CONFIG") != ""

		if local, ok := g.localServiceTarget(); ok && token == "" && !hasChosenConfig {
			return local, nil
		}

		return serverTarget{}, errors.New("no Relic server: run relic auth login <url>, or set --url or RELIC_URL")
	}

	if err := checkServerURL(url); err != nil {
		return serverTarget{}, err
	}

	return serverTarget{
		url:                  strings.TrimRight(url, "/"),
		token:                token,
		contextToken:         contextToken && token != "",
		certificateAuthority: certificateAuthority,
	}, nil
}

func checkServerURL(raw string) error {
	u, err := neturl.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return fmt.Errorf("%q is not an http(s) URL without credentials", raw)
	}

	return nil
}

func (g *globals) localServiceTarget() (serverTarget, bool) {
	configHome := g.getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(configHome) {
		configHome = filepath.Join(g.getenv("HOME"), ".config")
	}

	settingsFiles := []localSettings{
		{
			path:             filepath.Join(g.machineRoot, systemSettings),
			root:             g.machineRoot,
			defaultDirectory: filepath.Join(systemDataDir, ".config", "relic", "tls"),
		},
	}
	if filepath.IsAbs(configHome) {
		settingsFiles = slices.Insert(settingsFiles, 0, localSettings{
			path:             filepath.Join(configHome, "relic", "server.env"),
			defaultDirectory: filepath.Join(configHome, "relic", "tls"),
		})
	}

	for _, local := range settingsFiles {
		raw, err := os.ReadFile(local.path) //nolint:gosec // the settings file relic server setup wrote
		if err != nil {
			continue
		}

		settings := keyValues(string(raw), "=")

		url, ok := loopbackURL(cmp.Or(settings["RELIC_HTTP_LISTEN_ADDRESS"], defaultListenAddress))
		if !ok || settings["RELIC_ADMIN_PASSWORD"] == "" {
			continue
		}

		return serverTarget{
			url:                  url,
			username:             cmp.Or(settings["RELIC_ADMIN_USERNAME"], "admin"),
			password:             settings["RELIC_ADMIN_PASSWORD"],
			certificateAuthority: local.certificateAuthority(settings),
		}, true
	}

	return serverTarget{}, false
}

type localSettings struct {
	path             string
	root             string
	defaultDirectory string
}

func (l localSettings) certificateAuthority(settings map[string]string) string {
	directory := cmp.Or(settings["RELIC_TLS_DIRECTORY"], l.defaultDirectory)

	authority, err := os.ReadFile( //nolint:gosec // the certificate authority the local server keeps
		filepath.Join(l.root, directory, servertls.AuthorityCertificateFile),
	)
	if err != nil {
		return ""
	}

	return string(authority)
}

func loopbackURL(listenAddress string) (string, bool) {
	address, ok := loopbackAddress(listenAddress)
	if !ok {
		return "", false
	}

	return "https://" + address, true
}

// loopbackAddress is the host:port that reaches a listen address from this
// machine: a listener on every address answers on 127.0.0.1.
func loopbackAddress(listenAddress string) (string, bool) {
	host, port, err := net.SplitHostPort(listenAddress)
	if err != nil {
		return "", false
	}

	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}

	return net.JoinHostPort(host, port), true
}

type contextView struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	User    string `json:"user,omitempty"`
	Current bool   `json:"current"`
}

func contextViews(c cliConfig) []contextView {
	views := make([]contextView, 0, len(c.Contexts))

	for _, x := range c.Contexts {
		views = append(views, contextView{
			Name:    x.Name,
			URL:     x.URL,
			User:    x.User,
			Current: x.Name == c.Current,
		})
	}

	return views
}

func contextCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "context",
		Short: "List, show and switch contexts",
	}
	cmd.AddCommand(contextListCmd(g), contextShowCmd(g), &cobra.Command{
		Use:   "use <name>",
		Short: "Make a context current",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return g.updateConfig(func(c *cliConfig) error {
				if _, ok := c.find(args[0]); !ok {
					return fmt.Errorf("no context %q", args[0])
				}

				c.Current = args[0]

				return nil
			})
		},
	})

	return cmd
}

func contextListCmd(g *globals) *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the contexts, the current one marked (tokens are not shown)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, _, err := g.loadConfig()
			if err != nil {
				return err
			}

			views := contextViews(c)

			return printMessage(cmd.OutOrStdout(), output, views, func(w io.Writer) error {
				table := terminal.Table{
					Headers: []string{
						"CURRENT",
						"NAME",
						"URL",
						"USER",
					},
				}

				for _, x := range views {
					mark := ""
					if x.Current {
						mark = "*"
					}

					table.Rows = append(table.Rows, []string{
						mark,
						x.Name,
						x.URL,
						x.User,
					})
				}

				return table.Print(w)
			})
		},
	}
	outputFlag(cmd, &output)

	return cmd
}

func contextShowCmd(g *globals) *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:   "show",
		Short: "Show the current context (its token is not shown)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, _, err := g.loadConfig()
			if err != nil {
				return err
			}

			name := cmp.Or(g.context, c.Current)

			i := slices.IndexFunc(c.Contexts, func(x cliContext) bool {
				return x.Name == name
			})

			switch {
			case i < 0 && g.context != "":
				return fmt.Errorf("no context %q in the config file", g.context)
			case i < 0:
				return errors.New("no current context: run relic auth login <url>")
			}

			x := contextViews(c)[i]

			return printMessage(cmd.OutOrStdout(), output, x, func(w io.Writer) error {
				return terminal.Table{
					Headers: []string{
						"NAME",
						"URL",
						"USER",
					},
					Rows: [][]string{
						{
							x.Name,
							x.URL,
							x.User,
						},
					},
				}.Print(w)
			})
		},
	}
	outputFlag(cmd, &output)

	return cmd
}

func logoutCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "logout [<context>]",
		Short: "Revoke a context's token and forget the context (the current context by default)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var forgotten cliContext

			if err := g.updateConfig(func(c *cliConfig) error {
				name := cmp.Or(g.context, c.Current)
				if len(args) == 1 {
					name = args[0]
				}

				if name == "" {
					return errors.New("no current context to sign out of: name one (relic auth context list)")
				}

				saved, ok := c.find(name)
				if !ok {
					return fmt.Errorf("no context %q", name)
				}

				forgotten = saved
				c.Contexts = slices.DeleteFunc(c.Contexts, func(x cliContext) bool {
					return x.Name == name
				})

				if c.Current == name {
					c.Current = ""
				}

				return nil
			}); err != nil {
				return err
			}

			if err := revokeContextToken(cmd.Context(), forgotten); err != nil {
				_, _ = fmt.Fprintf(
					cmd.ErrOrStderr(),
					"relic: %v; the context is forgotten, but its token stays valid until it expires\n",
					err,
				)

				return nil
			}

			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Signed out of %s (context %s).\n", forgotten.URL, forgotten.Name)

			return nil
		},
	}
}
