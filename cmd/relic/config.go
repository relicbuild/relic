package main

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"

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

const defaultListenAddress = ":8090"

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

	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.yaml")
	if err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	defer func() {
		_ = os.Remove(tmp.Name())
	}()

	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write config: %w", err)
	}

	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write config: %w", err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	if err := os.Rename(tmp.Name(), path); err != nil {
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
	url                  string
	token                string
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
	url, token := g.getenv("RELIC_URL"), g.getenv("RELIC_TOKEN")

	var certificateAuthority string

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
			if url == "" && g.url == "" {
				url = ctx.URL
				certificateAuthority = ctx.CertificateAuthority
			}

			if token == "" {
				token = ctx.Token
			}
		} else if g.context != "" {
			return serverTarget{}, fmt.Errorf("no context %q in the config file", g.context)
		}
	}

	if g.url != "" {
		url = g.url
	}

	if g.token != "" {
		token = g.token
	}

	if url == "" {
		hasChosenConfig := g.config != "" || g.getenv("RELIC_CONFIG") != ""

		if local, ok := g.localServiceTarget(); ok && token == "" && !hasChosenConfig {
			return local, nil
		}

		return serverTarget{}, errors.New("no Relic server: run relic auth login <url>, or set --url or RELIC_URL")
	}

	return serverTarget{
		url:                  strings.TrimRight(url, "/"),
		token:                token,
		certificateAuthority: certificateAuthority,
	}, nil
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
	host, port, err := net.SplitHostPort(listenAddress)
	if err != nil {
		return "", false
	}

	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}

	return "https://" + net.JoinHostPort(host, port), true
}

func contextCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "context",
		Short: "List, show and switch contexts",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List the contexts, the current one marked (tokens are not shown)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, _, err := g.loadConfig()
			if err != nil {
				return err
			}

			table := terminal.Table{
				Headers: []string{
					"CURRENT",
					"NAME",
					"URL",
					"USER",
				},
			}

			for _, x := range c.Contexts {
				mark := ""
				if x.Name == c.Current {
					mark = "*"
				}

				table.Rows = append(table.Rows, []string{
					mark,
					x.Name,
					x.URL,
					x.User,
				})
			}

			return table.Print(cmd.OutOrStdout())
		},
	}, &cobra.Command{
		Use:   "show",
		Short: "Show the current context (its token is not shown)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, _, err := g.loadConfig()
			if err != nil {
				return err
			}

			name := cmp.Or(g.context, c.Current)

			x, ok := c.find(name)
			if !ok {
				return errors.New("no current context: run relic auth login <url>")
			}

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
			}.Print(cmd.OutOrStdout())
		},
	}, &cobra.Command{
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

func logoutCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "logout [<context>]",
		Short: "Forget a context and its token (the current context by default)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return g.updateConfig(func(c *cliConfig) error {
				name := cmp.Or(g.context, c.Current)
				if len(args) == 1 {
					name = args[0]
				}

				n := len(c.Contexts)

				c.Contexts = slices.DeleteFunc(c.Contexts, func(x cliContext) bool {
					return x.Name == name
				})
				if name == "" || len(c.Contexts) == n {
					return fmt.Errorf("no context %q", name)
				}

				if c.Current == name {
					c.Current = ""
				}

				return nil
			})
		},
	}
}
