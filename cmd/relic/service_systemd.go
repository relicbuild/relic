package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	systemUser       = "relic"
	systemBinary     = "/usr/local/bin/relic"
	systemDataDir    = "/var/lib/relic"
	systemTLSDir     = "/etc/relic/tls"
	systemSettings   = "/etc/relic/relic.env"
	systemUnit       = "/etc/systemd/system/relic.service"
	systemCompletion = "/usr/share/bash-completion/completions/relic"
)

// systemd runs the server as relic.service: a user unit with linger, or a
// system unit with its own user (Linux).
type systemd struct {
	m      *machineSetup
	system bool
}

func (s *systemd) initSystem() string {
	return "systemd"
}

func (s *systemd) tool() string {
	return "systemctl"
}

func (s *systemd) scope() string {
	if s.system {
		return "system"
	}

	return "user"
}

func (s *systemd) definition() string {
	if s.system {
		return filepath.Join(s.m.root, systemUnit)
	}

	return filepath.Join(s.m.xdg("XDG_CONFIG_HOME", ".config"), "systemd", "user", "relic.service")
}

func (s *systemd) settingsPath() string {
	if s.system {
		return filepath.Join(s.m.root, systemSettings)
	}

	return filepath.Join(s.m.configDir(), "server.env")
}

func (s *systemd) binary() string {
	unit, err := os.ReadFile(s.definition())
	if err != nil {
		return ""
	}

	for l := range strings.Lines(string(unit)) {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "ExecStart="); ok {
			binary, _, _ := strings.Cut(v, " ")

			return binary
		}
	}

	return ""
}

func (s *systemd) systemctl(args ...string) []string {
	command := []string{
		"systemctl",
	}
	if !s.system {
		command = append(command, "--user")
	}

	return append(command, args...)
}

func (s *systemd) installActions(ctx context.Context) ([]machineAction, error) {
	if s.system {
		return s.systemInstallActions(ctx)
	}

	m := s.m
	data := m.dataDir()

	env, err := m.settings(
		"server settings, with secrets",
		s.settingsPath(),
		data,
		filepath.Join(m.configDir(), "tls"),
		0o600,
	)
	if err != nil {
		return nil, err
	}

	unit := m.file(
		"systemd user unit",
		s.definition(),
		[]byte(systemdUnit(m.executable, s.settingsPath(), "")),
		0o644,
	)

	linger := machineAction{
		what:  "keep it running without a session, and start it at boot",
		state: "unchanged",
	}
	if !s.hasLinger(ctx) {
		linger.command = []string{
			"loginctl",
			"enable-linger",
			m.username,
		}
		linger.state = "run"
	}

	return []machineAction{
		m.directory("the server's database and files", data, 0o700),
		env,
		unit,
		{
			what:    "systemd",
			command: s.systemctl("daemon-reload"),
			state:   "run",
		},
		{
			what:    "start at login",
			command: s.systemctl("enable", "relic.service"),
			state:   "run",
		},
		s.startAction(ctx, m.executable, env, unit),
		linger,
	}, nil
}

func (s *systemd) systemInstallActions(ctx context.Context) ([]machineAction, error) {
	m := s.m

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

	env, err := m.settings("server settings, with secrets", s.settingsPath(), systemDataDir, systemTLSDir, 0o640)
	if err != nil {
		return nil, err
	}

	unit := m.file(
		"systemd unit",
		s.definition(),
		[]byte(systemdUnit(systemBinary, systemSettings, systemUser)),
		0o644,
	)

	script, err := m.completion("bash")
	if err != nil {
		return nil, fmt.Errorf("setup: completion: %w", err)
	}

	return append(actions,
		env,
		s.settingsOwnerAction(),
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
			what:    "systemd",
			command: s.systemctl("daemon-reload"),
			state:   "run",
		},
		machineAction{
			what:    "start at boot",
			command: s.systemctl("enable", "relic.service"),
			state:   "run",
		},
		s.startAction(ctx, filepath.Join(m.root, systemBinary), env, unit, binary),
	), nil
}

func (s *systemd) settingsOwnerAction() machineAction {
	return machineAction{
		what: "the relic group reads the settings",
		command: []string{
			"chown",
			"root:" + systemUser,
			s.settingsPath(),
		},
		state: "run",
	}
}

func (s *systemd) startAction(ctx context.Context, binary string, changed ...machineAction) machineAction {
	verb := "start"

	for _, a := range changed {
		if a.state != "unchanged" {
			verb = "restart"
		}
	}

	if verb == "start" && s.runsAnotherBinary(ctx, binary) {
		verb = "restart"
	}

	return machineAction{
		what:    verb + " the server",
		command: s.systemctl(verb, "relic.service"),
		state:   "run",
	}
}

func (s *systemd) runsAnotherBinary(ctx context.Context, binary string) bool {
	command := s.systemctl("show", "relic.service", "--property=MainPID", "--value")

	out, err := s.m.query(ctx, command[0], command[1:]...)
	if err != nil {
		return false
	}

	pid, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil || pid <= 0 {
		return false
	}

	running, err := os.Stat(filepath.Join(s.m.proc, strconv.Itoa(pid), "exe"))
	if err != nil {
		return false
	}

	onDisk, err := os.Stat(binary)

	return err == nil && !os.SameFile(running, onDisk)
}

func (s *systemd) hasLinger(ctx context.Context) bool {
	m := s.m

	out, err := m.query(ctx, "loginctl", "show-user", m.username, "--property=Linger", "--value")
	if err == nil && strings.TrimSpace(out) == "yes" {
		return true
	}

	if _, err := os.Stat(filepath.Join(m.root, "var", "lib", "systemd", "linger", m.username)); err == nil {
		return true
	}

	return false
}

func systemdUnit(executable, settings, user string) string {
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

func (s *systemd) uninstallActions(context.Context) (remove, keep []machineAction) {
	m := s.m

	if exists(s.definition()) {
		what := "systemd user unit"
		if s.system {
			what = "systemd unit"
		}

		remove = append(remove,
			machineAction{
				what:    "stop the server",
				command: s.systemctl("disable", "--now", "relic.service"),
				state:   "run",
			},
			removal(what, s.definition()),
			machineAction{
				what:    "systemd",
				command: s.systemctl("daemon-reload"),
				state:   "run",
			},
		)
	}

	if !s.system {
		return remove, nil
	}

	remove = append(remove, existing(
		removal("relic binary", filepath.Join(m.root, systemBinary)),
		removal("bash completion for every user", filepath.Join(m.root, systemCompletion)),
	)...)

	keep = existing(
		removal("server settings, with the encryption key", s.settingsPath()),
		removal("the server's certificate authority and certificate", filepath.Join(m.root, systemTLSDir)),
		removal("the server's database and files", filepath.Join(m.root, systemDataDir)),
	)

	if m.hasUser(systemUser) {
		keep = append(keep, machineAction{
			what: "system user owning the data",
			command: []string{
				"userdel",
				systemUser,
			},
			state: "run",
		})
	}

	return remove, keep
}

func (s *systemd) status(ctx context.Context) (serviceState, error) {
	command := s.systemctl(
		"show",
		"relic.service",
		"--property=ActiveState,SubState,UnitFileState,MainPID,NRestarts,ActiveEnterTimestamp",
	)

	out, err := s.m.query(ctx, command[0], command[1:]...)
	if err != nil {
		return serviceState{}, err
	}

	p := keyValues(out, "=")
	state := serviceState{
		active:  p["ActiveState"],
		detail:  p["SubState"],
		enabled: p["UnitFileState"],
		since:   p["ActiveEnterTimestamp"],
	}
	state.pid, _ = strconv.Atoi(p["MainPID"])
	state.restarts, _ = strconv.Atoi(p["NRestarts"])

	return state, nil
}

func (s *systemd) stop(ctx context.Context) error {
	command := s.systemctl("stop", "relic.service")

	return s.m.run(ctx, command[0], command[1:]...)
}

func (s *systemd) restart(ctx context.Context) error {
	if s.system {
		if err := s.m.apply(ctx, s.settingsOwnerAction()); err != nil {
			return fmt.Errorf("give the relic group the settings: %w", err)
		}
	}

	command := s.systemctl("restart", "relic.service")

	return s.m.run(ctx, command[0], command[1:]...)
}

func (s *systemd) startCommand() string {
	return strings.Join(s.systemctl("start", "relic.service"), " ")
}

func (s *systemd) statusCommand() string {
	return strings.Join(s.systemctl("status", "relic"), " ")
}

func (s *systemd) logsCommand() string {
	if s.system {
		return "journalctl -u relic -f"
	}

	return "journalctl --user -u relic -f"
}
