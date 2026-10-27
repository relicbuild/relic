package main

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"relic/internal/platform/terminal"
)

type machineSetup struct {
	home         string
	root         string
	goos         string
	shell        string
	listen       string
	listenSet    bool
	system       bool
	yes          bool
	configPath   string
	username     string
	euid         int
	executable   string
	getenv       func(string) string
	completion   func(shell string) (string, error)
	run          func(ctx context.Context, name string, args ...string) error
	query        func(ctx context.Context, name string, args ...string) (string, error)
	proc         string
	hasUser      func(name string) bool
	lookPath     func(name string) (string, error)
	homeOf       func(username string) string
	portInUse    func(ctx context.Context, address string) bool
	ready        func(ctx context.Context, url, certificateAuthority string) error
	readyTimeout time.Duration
	in           *bufio.Reader
	out          io.Writer
}

type machineAction struct {
	what    string
	path    string
	content []byte
	mode    fs.FileMode
	command []string
	dir     bool
	state   string
	// done, when set, reports whether the command's effect has taken hold;
	// apply waits up to settleTimeout for it.
	done func(ctx context.Context) bool
}

const settleTimeout = 10 * time.Second

const (
	readyTimeout      = time.Minute
	readyPollInterval = 500 * time.Millisecond
)

const setupMetricsListenAddress = "127.0.0.1:8091"

func (m *machineSetup) runSetup(ctx context.Context) error {
	if _, err := m.serviceManager(); err != nil {
		return fmt.Errorf("setup: %w", err)
	}

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

	if err := m.refuseAnotherScope(); err != nil {
		return err
	}

	if err := m.refusePortsInUse(ctx); err != nil {
		return err
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

	if err := m.waitReady(ctx); err != nil {
		return err
	}

	m.printNext()

	return nil
}

func (m *machineSetup) refuseAnotherScope() error {
	if m.goos != "linux" {
		return nil
	}

	if m.system {
		return m.refuseUserService()
	}

	svc, err := m.serviceManagerAt(true)
	if err != nil || !exists(svc.definition()) {
		return err
	}

	return fmt.Errorf(
		"setup: the system service %s already runs a Relic server on this machine, and a user service "+
			"would collide with it on the same ports; manage it with sudo, or remove it first with "+
			"sudo relic server uninstall --system",
		svc.definition(),
	)
}

func (m *machineSetup) refuseUserService() error {
	owner := m.getenv("SUDO_USER")
	if owner == "" || owner == "root" {
		return nil
	}

	home := m.homeOf(owner)
	if home == "" {
		return nil
	}

	unit := filepath.Join(home, ".config", "systemd", "user", "relic.service")
	if !exists(unit) {
		return nil
	}

	return fmt.Errorf(
		"setup: the user service %s already runs a Relic server for %s on this machine, and a system service "+
			"would collide with it on the same ports; remove it first with relic server uninstall, as %s",
		unit,
		owner,
		owner,
	)
}

func (m *machineSetup) refusePortsInUse(ctx context.Context) error {
	svc, err := m.serviceManager()
	if err != nil {
		return fmt.Errorf("setup: %w", err)
	}

	if exists(svc.definition()) {
		return nil
	}

	settings := m.settingsValues()

	listen := m.listen
	if !m.listenSet {
		listen = cmp.Or(settings["RELIC_HTTP_LISTEN_ADDRESS"], m.listen)
	}

	metrics := cmp.Or(settings["RELIC_METRICS_LISTEN_ADDRESS"], setupMetricsListenAddress)

	for _, address := range []string{
		listen,
		metrics,
	} {
		if address == "off" || !m.portInUse(ctx, address) {
			continue
		}

		return fmt.Errorf(
			"setup: %s is in use: another program, or a Relic server relic server setup did not install, "+
				"listens there; stop it first, or choose a free address (--listen for the server, "+
				"RELIC_METRICS_LISTEN_ADDRESS in %s for metrics)",
			address,
			m.tilde(svc.settingsPath()),
		)
	}

	return nil
}

func (m *machineSetup) waitReady(ctx context.Context) error {
	svc, err := m.serviceManager()
	if err != nil {
		return fmt.Errorf("setup: %w", err)
	}

	url, ok := loopbackURL(m.listenSetting())
	if !ok {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, m.readyTimeout)
	defer cancel()

	for {
		err := m.ready(ctx, url, m.localSettings().certificateAuthority(m.settingsValues()))
		if err == nil {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf(
				"setup: the service was started, but %s/readyz did not answer within %s: %w; "+
					"read its logs with %s",
				url,
				m.readyTimeout,
				err,
				svc.logsCommand(),
			)
		case <-time.After(readyPollInterval):
		}
	}
}

func (m *machineSetup) findCommands(actions []machineAction) error {
	for _, a := range actions {
		if a.command == nil {
			continue
		}

		if _, err := m.lookPath(a.command[0]); err != nil {
			return fmt.Errorf(
				"%s is not on PATH: relic server setup needs it to run the server as a service; "+
					"without it, run relic server start by hand",
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
	svc, err := m.serviceManager()
	if err != nil {
		return nil, err
	}

	var actions []machineAction

	if !m.system {
		a, err := m.userActions()
		if err != nil {
			return nil, err
		}

		actions = append(actions, a...)
	}

	a, err := svc.installActions(ctx)
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
			setupMetricsListenAddress,
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

func (m *machineSetup) directory(what, path string, mode fs.FileMode) machineAction {
	a := machineAction{
		what:  what,
		path:  path,
		mode:  mode,
		dir:   true,
		state: "create",
	}
	if fi, err := os.Stat(path); err == nil && fi.IsDir() {
		a.state = "unchanged"
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

		if a.done != nil {
			if err := m.settle(ctx, a); err != nil {
				return fmt.Errorf("setup: %w", err)
			}
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

	if err := writeFileAtomic(a.path, bytes.NewReader(a.content), a.mode); err != nil {
		return fmt.Errorf("setup: write %s: %w", a.path, err)
	}

	return nil
}

func (m *machineSetup) settle(ctx context.Context, a machineAction) error {
	ctx, cancel := context.WithTimeout(ctx, settleTimeout)
	defer cancel()

	for !a.done(ctx) {
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s: %s did not take effect within %s", strings.Join(a.command, " "), a.what,
				settleTimeout)
		case <-time.After(100 * time.Millisecond):
		}
	}

	return nil
}

func (m *machineSetup) printNext() {
	svc, err := m.serviceManager()
	if err != nil {
		return
	}

	settings, status, logs := svc.settingsPath(), svc.statusCommand(), svc.logsCommand()

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
	return cmp.Or(m.settingsValues()["RELIC_HTTP_LISTEN_ADDRESS"], m.listen)
}

func (m *machineSetup) settingsValues() map[string]string {
	b, err := os.ReadFile(m.localSettings().path) //nolint:gosec // the settings file relic server setup wrote
	if err != nil {
		return map[string]string{}
	}

	return keyValues(string(b), "=")
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
		goos:       runtime.GOOS,
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
			//nolint:gosec // G204: systemctl, launchctl, loginctl, useradd and chown with arguments relic server setup builds
			c := exec.CommandContext(ctx, name, args...)
			c.Stdout, c.Stderr = cmd.ErrOrStderr(), cmd.ErrOrStderr()

			return c.Run()
		},
		query: func(ctx context.Context, name string, args ...string) (string, error) {
			//nolint:gosec // G204: systemctl show, launchctl print and ps
			b, err := exec.CommandContext(ctx, name, args...).Output()

			return string(b), err
		},
		proc: "/proc",
		hasUser: func(name string) bool {
			_, err := user.Lookup(name)

			return err == nil
		},
		lookPath: exec.LookPath,
		homeOf: func(username string) string {
			u, err := user.Lookup(username)
			if err != nil {
				return ""
			}

			return u.HomeDir
		},
		portInUse:    portInUse,
		ready:        serverReady,
		readyTimeout: readyTimeout,
		in:           bufio.NewReader(cmd.InOrStdin()),
		out:          out,
	}, nil
}

func portInUse(ctx context.Context, address string) bool {
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", address)
	if err != nil {
		return errors.Is(err, syscall.EADDRINUSE)
	}

	_ = listener.Close()

	loopback, ok := loopbackAddress(address)
	if !ok {
		return false
	}

	conn, err := (&net.Dialer{
		Timeout: time.Second,
	}).DialContext(ctx, "tcp", loopback)
	if err != nil {
		return false
	}

	_ = conn.Close()

	return true
}

func serverReady(ctx context.Context, url, certificateAuthority string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	transport := http.DefaultTransport
	if certificateAuthority != "" {
		transport = trustingTransport(certificateAuthority)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/readyz", nil)
	if err != nil {
		return err
	}

	resp, err := (&http.Client{
		Transport: transport,
	}).Do(req)
	if err != nil {
		return err
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))

		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	return nil
}
