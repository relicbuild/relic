package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"relic/internal/platform/terminal"
)

type machineSetup struct {
	home       string
	root       string
	shell      string
	listen     string
	listenSet  bool
	system     bool
	yes        bool
	configPath string
	username   string
	euid       int
	executable string
	getenv     func(string) string
	completion func(shell string) (string, error)
	run        func(ctx context.Context, name string, args ...string) error
	query      func(ctx context.Context, name string, args ...string) (string, error)
	proc       string
	hasUser    func(name string) bool
	lookPath   func(name string) (string, error)
	in         *bufio.Reader
	out        io.Writer
}

type machineAction struct {
	what    string
	path    string
	content []byte
	mode    fs.FileMode
	command []string
	dir     bool
	state   string
}

const (
	systemUser       = "relic"
	systemBinary     = "/usr/local/bin/relic"
	systemDataDir    = "/var/lib/relic"
	systemTLSDir     = "/etc/relic/tls"
	systemSettings   = "/etc/relic/relic.env"
	systemUnit       = "/etc/systemd/system/relic.service"
	systemCompletion = "/usr/share/bash-completion/completions/relic"
)

func (m *machineSetup) runSetup(ctx context.Context) error {
	if !m.system && m.home == "" {
		return errors.New("setup: no home directory: set HOME or pass --home")
	}

	switch m.shell {
	case "bash", "zsh", "fish":
	default:
		return fmt.Errorf("setup: shell %q: use bash, zsh or fish", m.shell)
	}

	if m.system && m.euid != 0 {
		return errors.New("setup: --system creates a user and writes /etc: run it with sudo")
	}

	if _, _, err := net.SplitHostPort(m.listen); m.listenSet && err != nil {
		return fmt.Errorf("setup: --listen %q must be host:port or :port", m.listen)
	}

	if strings.Contains(m.executable, "go-build") {
		return errors.New(
			"setup: this relic runs from a temporary go run build; install it (go install ./cmd/relic) and run that",
		)
	}

	actions, err := m.plan(ctx)
	if err != nil {
		return err
	}

	if err := m.findCommands(actions); err != nil {
		return fmt.Errorf("setup: %w", err)
	}

	m.printPlan("relic server setup will:", actions)

	if !m.yes {
		ok, err := m.ask("Proceed? [Y/n] ", true)
		if err != nil {
			return err
		}

		if !ok {
			return errors.New("setup: cancelled; nothing was changed")
		}
	}

	for _, a := range actions {
		if err := m.apply(ctx, a); err != nil {
			return err
		}
	}

	m.printNext()

	return nil
}

func (m *machineSetup) findCommands(actions []machineAction) error {
	for _, a := range actions {
		if a.command == nil {
			continue
		}

		if _, err := m.lookPath(a.command[0]); err != nil {
			return fmt.Errorf(
				"%s is not on PATH: relic server setup runs the server as a systemd service; "+
					"without systemd (macOS, Windows), run relic server start by hand",
				a.command[0],
			)
		}
	}

	return nil
}

func (m *machineSetup) ask(question string, def bool) (bool, error) {
	_, _ = fmt.Fprint(m.out, question)

	line, err := m.in.ReadString('\n')
	if err != nil && (!errors.Is(err, io.EOF) || line == "") {
		return false, errors.New("no answer on stdin; pass --yes")
	}

	switch strings.ToLower(strings.TrimSpace(line)) {
	case "":
		return def, nil
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

func (m *machineSetup) plan(ctx context.Context) ([]machineAction, error) {
	var actions []machineAction

	if !m.system {
		a, err := m.userActions()
		if err != nil {
			return nil, err
		}

		actions = append(actions, a...)
	}

	var (
		a   []machineAction
		err error
	)
	if m.system {
		a, err = m.systemServiceActions(ctx)
	} else {
		a, err = m.userServiceActions(ctx)
	}

	if err != nil {
		return nil, err
	}

	return append(actions, a...), nil
}

func (m *machineSetup) localSettings() localSettings {
	if m.system {
		return localSettings{
			path:             filepath.Join(m.root, systemSettings),
			root:             m.root,
			defaultDirectory: filepath.Join(systemDataDir, ".config", "relic", "tls"),
		}
	}

	return localSettings{
		path:             filepath.Join(m.configDir(), "server.env"),
		defaultDirectory: filepath.Join(m.configDir(), "tls"),
	}
}

func (m *machineSetup) configDir() string {
	return filepath.Join(m.xdg("XDG_CONFIG_HOME", ".config"), "relic")
}

func (m *machineSetup) dataDir() string {
	return filepath.Join(m.xdg("XDG_DATA_HOME", filepath.Join(".local", "share")), "relic")
}

func (m *machineSetup) xdg(env, fallback string) string {
	if d := m.getenv(env); filepath.IsAbs(d) {
		return d
	}

	return filepath.Join(m.home, fallback)
}

func (m *machineSetup) userActions() ([]machineAction, error) {
	actions := []machineAction{}

	if _, err := os.Stat(m.configPath); errors.Is(err, fs.ErrNotExist) {
		actions = append(actions, machineAction{
			what:    "client config: contexts and tokens",
			path:    m.configPath,
			content: []byte{},
			mode:    0o600,
			state:   "create",
		})
	} else {
		actions = append(actions, machineAction{
			what:  "client config: contexts and tokens",
			path:  m.configPath,
			state: "unchanged",
		})
	}

	script, err := m.completion(m.shell)
	if err != nil {
		return nil, fmt.Errorf("setup: completion: %w", err)
	}

	switch m.shell {
	case "fish":
		p := filepath.Join(m.xdg("XDG_CONFIG_HOME", ".config"), "fish", "completions", "relic.fish")
		actions = append(actions, m.file("fish completion", p, []byte(script), 0o644))
	default:
		p := filepath.Join(m.configDir(), "completion."+m.shell)
		actions = append(actions, m.file(m.shell+" completion", p, []byte(script), 0o644))

		q := "'" + p + "'"
		if rest, ok := strings.CutPrefix(p, m.home+"/"); ok {
			q = `"$HOME/` + rest + `"`
		}

		rc, load := filepath.Join(m.home, ".bashrc"), "[ -f "+q+" ] && . "+q
		if m.shell == "zsh" {
			rc = filepath.Join(m.home, ".zshrc")
			load = "(( ${+functions[compdef]} )) || { autoload -Uz compinit && compinit; }\n" +
				"[ -f " + q + " ] && source " + q
		}

		a, err := m.block("loads the completion", rc, "# Completion for relic, written by relic server setup.\n"+load)
		if err != nil {
			return nil, err
		}

		actions = append(actions, a)
	}

	return actions, nil
}

func (m *machineSetup) userServiceActions(ctx context.Context) ([]machineAction, error) {
	settings := filepath.Join(m.configDir(), "server.env")
	data := m.dataDir()

	env, err := m.settings("server settings, with secrets", settings, data, filepath.Join(m.configDir(), "tls"), 0o600)
	if err != nil {
		return nil, err
	}

	unit := m.file(
		"systemd user unit",
		filepath.Join(m.xdg("XDG_CONFIG_HOME", ".config"), "systemd", "user", "relic.service"),
		[]byte(serviceUnit(m.executable, settings, "")),
		0o644,
	)

	dir := machineAction{
		what:  "the server's database and files",
		path:  data,
		mode:  0o700,
		dir:   true,
		state: "create",
	}
	if fi, err := os.Stat(data); err == nil && fi.IsDir() {
		dir.state = "unchanged"
	}

	return []machineAction{
		dir,
		env,
		unit,
		{
			what: "systemd",
			command: []string{
				"systemctl",
				"--user",
				"daemon-reload",
			},
			state: "run",
		},
		{
			what: "start at login",
			command: []string{
				"systemctl",
				"--user",
				"enable",
				"relic.service",
			},
			state: "run",
		},
		m.startAction(ctx, []string{
			"systemctl",
			"--user",
		}, m.executable, env, unit),
		{
			what: "keep it running without a session, and start it at boot",
			command: []string{
				"loginctl",
				"enable-linger",
				m.username,
			},
			state: "run",
		},
	}, nil
}

func (m *machineSetup) systemServiceActions(ctx context.Context) ([]machineAction, error) {
	var actions []machineAction

	binary := machineAction{
		state: "unchanged",
	}

	if !m.hasUser(systemUser) {
		actions = append(actions, machineAction{
			what: "system user owning the data",
			command: []string{
				"useradd",
				"--system",
				"--user-group",
				"--create-home",
				"--home-dir",
				systemDataDir,
				"--shell",
				"/usr/sbin/nologin",
				systemUser,
			},
			state: "run",
		})
	} else if _, err := os.Stat(filepath.Join(m.root, systemDataDir)); errors.Is(err, fs.ErrNotExist) {
		actions = append(actions, machineAction{
			what: "the server's database and files",
			command: []string{
				"install",
				"-d",
				"-o",
				systemUser,
				"-g",
				systemUser,
				"-m",
				"0700",
				systemDataDir,
			},
			state: "run",
		})
	}

	if m.executable != filepath.Join(m.root, systemBinary) {
		b, err := os.ReadFile(m.executable)
		if err != nil {
			return nil, fmt.Errorf("setup: read this binary: %w", err)
		}

		binary = m.file("relic binary", filepath.Join(m.root, systemBinary), b, 0o755)
		actions = append(actions, binary)
	}

	env, err := m.settings(
		"server settings, with secrets",
		filepath.Join(m.root, systemSettings),
		systemDataDir,
		systemTLSDir,
		0o640,
	)
	if err != nil {
		return nil, err
	}

	unit := m.file(
		"systemd unit",
		filepath.Join(m.root, systemUnit),
		[]byte(serviceUnit(systemBinary, systemSettings, systemUser)),
		0o644,
	)

	script, err := m.completion("bash")
	if err != nil {
		return nil, fmt.Errorf("setup: completion: %w", err)
	}

	return append(actions,
		env,
		machineAction{
			what: "the relic group reads the settings",
			command: []string{
				"chown",
				"root:" + systemUser,
				filepath.Join(m.root, systemSettings),
			},
			state: "run",
		},
		machineAction{
			what: "the server's TLS certificates",
			command: []string{
				"install",
				"-d",
				"-o",
				systemUser,
				"-g",
				systemUser,
				"-m",
				"0750",
				systemTLSDir,
			},
			state: "run",
		},
		unit,
		m.file("bash completion for every user", filepath.Join(m.root, systemCompletion), []byte(script), 0o644),
		machineAction{
			what: "systemd",
			command: []string{
				"systemctl",
				"daemon-reload",
			},
			state: "run",
		},
		machineAction{
			what: "start at boot",
			command: []string{
				"systemctl",
				"enable",
				"relic.service",
			},
			state: "run",
		},
		m.startAction(ctx, []string{
			"systemctl",
		}, filepath.Join(m.root, systemBinary), env, unit, binary),
	), nil
}

func (m *machineSetup) startAction(
	ctx context.Context,
	systemctl []string,
	binary string,
	changed ...machineAction,
) machineAction {
	verb := "start"

	for _, a := range changed {
		if a.state != "unchanged" {
			verb = "restart"
		}
	}

	if verb == "start" && m.runsAnotherBinary(ctx, systemctl, binary) {
		verb = "restart"
	}

	return machineAction{
		what:    verb + " the server",
		command: append(slices.Clone(systemctl), verb, "relic.service"),
		state:   "run",
	}
}

func (m *machineSetup) runsAnotherBinary(ctx context.Context, systemctl []string, binary string) bool {
	out, err := m.query(ctx, systemctl[0], append(slices.Clone(systemctl[1:]),
		"show",
		"relic.service",
		"--property=MainPID",
		"--value",
	)...)
	if err != nil {
		return false
	}

	pid, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil || pid <= 0 {
		return false
	}

	running, err := os.Stat(filepath.Join(m.proc, strconv.Itoa(pid), "exe"))
	if err != nil {
		return false
	}

	onDisk, err := os.Stat(binary)

	return err == nil && !os.SameFile(running, onDisk)
}

func serviceUnit(executable, settings, user string) string {
	var b strings.Builder

	b.WriteString("# Written by relic server setup; relic server setup rewrites it.\n")
	b.WriteString("[Unit]\nDescription=Relic artifact repository\n")

	if user != "" {
		b.WriteString("After=network-online.target\nWants=network-online.target\n")
	}

	b.WriteString("\n[Service]\n")
	_, _ = fmt.Fprintf(&b, "ExecStart=%s server start --worker\n", executable)
	_, _ = fmt.Fprintf(&b, "EnvironmentFile=%s\n", settings)
	b.WriteString("Restart=always\nRestartSec=3\nLimitNOFILE=65536\n")

	if user != "" {
		_, _ = fmt.Fprintf(&b, "User=%s\nGroup=%s\n", user, user)
		b.WriteString("NoNewPrivileges=true\nPrivateTmp=true\nProtectSystem=strict\nProtectHome=true\n")
		_, _ = fmt.Fprintf(&b, "ReadWritePaths=%s %s\n", systemDataDir, systemTLSDir)
		b.WriteString("\n[Install]\nWantedBy=multi-user.target\n")
	} else {
		b.WriteString("\n[Install]\nWantedBy=default.target\n")
	}

	return b.String()
}

// Settings already in it stay as they are (the encryption key must never change under existing secrets).
func (m *machineSetup) settings(what, path, data, tlsDirectory string, mode fs.FileMode) (machineAction, error) {
	old, err := os.ReadFile(path) //nolint:gosec // the settings file relic server setup writes
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return machineAction{}, fmt.Errorf("setup: %w", err)
	}

	key, err := randomBase64(32)
	if err != nil {
		return machineAction{}, err
	}

	password, err := randomPassword()
	if err != nil {
		return machineAction{}, err
	}

	defaults := [][2]string{
		{
			"RELIC_HTTP_LISTEN_ADDRESS",
			m.listen,
		},
		{
			"RELIC_METRICS_LISTEN_ADDRESS",
			"127.0.0.1:8091",
		},
		{
			"RELIC_DATABASE_URL",
			"sqlite://" + filepath.Join(data, "relic.db"),
		},
		{
			"RELIC_OBJECT_STORAGE_TYPE",
			"filesystem",
		},
		{
			"RELIC_OBJECT_STORAGE_PATH",
			filepath.Join(data, "objects"),
		},
		{
			"RELIC_SCAN_VULNERABILITY_DATABASE_DIR",
			filepath.Join(data, "grype"),
		},
		{
			"RELIC_TLS_DIRECTORY",
			tlsDirectory,
		},
		{
			"RELIC_ENCRYPTION_KEY",
			key,
		},
		{
			"RELIC_ADMIN_PASSWORD",
			password,
		},
	}

	content := string(old)
	if content == "" {
		content = "# Relic server settings, written by relic server setup. Settings already here are kept;\n" +
			"# the Configuration page of Relic's documentation lists the rest.\n"
	}

	for _, kv := range defaults {
		content = withSetting(content, kv[0], kv[1], kv[0] == "RELIC_HTTP_LISTEN_ADDRESS" && m.listenSet)
	}

	return m.file(what, path, []byte(content), mode), nil
}

func withSetting(content, key, value string, replace bool) string {
	found := false

	lines := strings.SplitAfter(content, "\n")
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimLeft(l, " \t"), key+"=") {
			found = true

			if replace {
				lines[i] = key + "=" + value + "\n"
			}
		}
	}

	if found {
		return strings.Join(lines, "")
	}

	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}

	return content + key + "=" + value + "\n"
}

func randomBase64(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("setup: random: %w", err)
	}

	return base64.StdEncoding.EncodeToString(b), nil
}

// randomPassword is URL-safe, since clients put credentials into URLs unescaped.
func randomPassword() (string, error) {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("setup: random: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (m *machineSetup) file(what, path string, content []byte, mode fs.FileMode) machineAction {
	a := machineAction{
		what:    what,
		path:    path,
		content: content,
		mode:    mode,
		state:   "create",
	}

	old, err := os.ReadFile(path) //nolint:gosec // a file relic server setup writes
	if err == nil {
		a.state = "update"

		if fi, err := os.Stat(path); err == nil && bytes.Equal(old, content) && fi.Mode().Perm() == mode {
			a.state = "unchanged"
		}
	}

	return a
}

func (m *machineSetup) block(what, path, content string) (machineAction, error) {
	old, err := os.ReadFile(path) //nolint:gosec // the user's shell rc file
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return machineAction{}, fmt.Errorf("setup: %w", err)
	}

	mode := fs.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}

	content, err = withBlock(string(old), "completion", content)
	if err != nil {
		return machineAction{}, fmt.Errorf("setup: %s: %w", path, err)
	}

	return m.file(what, path, []byte(content), mode), nil
}

func (m *machineSetup) printPlan(title string, actions []machineAction) {
	plan := terminal.Table{
		Headers: []string{
			"STATE",
			"TARGET",
			"WHAT",
		},
	}

	for _, a := range actions {
		target := styleValue.Render(m.tilde(a.path))
		if a.command != nil {
			target = styleRun.Render("$ " + strings.Join(a.command, " "))
		}

		plan.Rows = append(plan.Rows, []string{
			stateStyle(a.state).Render(a.state),
			target,
			styleMuted.Render(a.what),
		})
	}

	printStyled(m.out, styleTitle.Render(title))
	_ = plan.Print(m.out)
}

func (m *machineSetup) apply(ctx context.Context, a machineAction) error {
	if a.command != nil {
		if err := m.run(ctx, a.command[0], a.command[1:]...); err != nil {
			return fmt.Errorf("setup: %s: %w", strings.Join(a.command, " "), err)
		}

		return nil
	}

	if a.state == "unchanged" {
		return nil
	}

	if a.state == "remove" {
		if err := os.RemoveAll(a.path); err != nil {
			return fmt.Errorf("uninstall: %w", err)
		}

		return nil
	}

	if a.dir {
		if err := os.MkdirAll(a.path, a.mode); err != nil {
			return fmt.Errorf("setup: %w", err)
		}

		return nil
	}

	if err := os.MkdirAll(filepath.Dir(a.path), 0o755); err != nil { //nolint:gosec // G301: config dirs others read
		return fmt.Errorf("setup: %w", err)
	}

	// A file with secrets is written whole under a temporary name and
	// renamed, so it is never readable with a wider mode in between.
	tmp, err := os.CreateTemp(filepath.Dir(a.path), ".relic-setup-*")
	if err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	defer func() {
		_ = os.Remove(tmp.Name())
	}()

	if _, err := tmp.Write(a.content); err != nil {
		_ = tmp.Close()

		return fmt.Errorf("setup: write %s: %w", a.path, err)
	}

	if err := tmp.Chmod(a.mode); err != nil {
		_ = tmp.Close()

		return fmt.Errorf("setup: %w", err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("setup: %w", err)
	}

	if err := os.Rename(tmp.Name(), a.path); err != nil {
		return fmt.Errorf("setup: %w", err)
	}

	return nil
}

func (m *machineSetup) printNext() {
	settings := filepath.Join(m.configDir(), "server.env")
	status := "systemctl --user status relic"
	logs := "journalctl --user -u relic -f"

	if m.system {
		settings = systemSettings
		status = "systemctl status relic"
		logs = "journalctl -u relic -f"
	}

	listen := m.listenSetting()

	address, ok := loopbackURL(listen)
	if !ok {
		address = "https://" + listen
	}

	_, _ = fmt.Fprintf(m.out, "Done. The server listens on %s (%s, %s).\n", listen, status, logs)
	_, _ = fmt.Fprintf(m.out,
		"Sign in with relic auth login %s --user admin; the first password is RELIC_ADMIN_PASSWORD in %s.\n",
		address,
		m.tilde(settings),
	)
}

func (m *machineSetup) listenSetting() string {
	path := filepath.Join(m.configDir(), "server.env")
	if m.system {
		path = filepath.Join(m.root, systemSettings)
	}

	b, err := os.ReadFile(path) //nolint:gosec // the settings file relic server setup wrote
	if err == nil {
		if v := keyValues(string(b), "=")["RELIC_HTTP_LISTEN_ADDRESS"]; v != "" {
			return v
		}
	}

	return m.listen
}

func (m *machineSetup) tilde(p string) string {
	if rest, ok := strings.CutPrefix(p, m.home+"/"); ok && m.home != "" {
		return "~/" + rest
	}

	return p
}

func newMachineSetup(cmd *cobra.Command, g *globals, home, root string) (*machineSetup, error) {
	configPath, err := g.configPath()
	if err != nil {
		return nil, err
	}

	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("setup: find this binary: %w", err)
	}

	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}

	username := g.getenv("USER")
	if username == "" {
		username = g.getenv("LOGNAME")
	}

	shell := filepath.Base(g.getenv("SHELL"))
	if shell != "zsh" && shell != "fish" {
		shell = "bash"
	}

	out := cmd.OutOrStdout()

	return &machineSetup{
		home:       home,
		root:       root,
		shell:      shell,
		configPath: configPath,
		username:   username,
		euid:       os.Geteuid(),
		executable: exe,
		getenv:     g.getenv,
		completion: func(shell string) (string, error) {
			var b bytes.Buffer

			var err error

			switch shell {
			case "zsh":
				err = cmd.Root().GenZshCompletion(&b)
			case "fish":
				err = cmd.Root().GenFishCompletion(&b, true)
			default:
				err = cmd.Root().GenBashCompletionV2(&b, true)
			}

			return b.String(), err
		},
		run: func(ctx context.Context, name string, args ...string) error {
			//nolint:gosec // G204: systemctl, loginctl, useradd and chown with arguments relic server setup builds
			c := exec.CommandContext(ctx, name, args...)
			c.Stdout, c.Stderr = cmd.ErrOrStderr(), cmd.ErrOrStderr()

			return c.Run()
		},
		query: func(ctx context.Context, name string, args ...string) (string, error) {
			b, err := exec.CommandContext(ctx, name, args...).Output() //nolint:gosec // G204: systemctl show

			return string(b), err
		},
		proc: "/proc",
		hasUser: func(name string) bool {
			_, err := user.Lookup(name)

			return err == nil
		},
		lookPath: exec.LookPath,
		in:       bufio.NewReader(cmd.InOrStdin()),
		out:      out,
	}, nil
}
