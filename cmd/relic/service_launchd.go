package main

import (
	"context"
	"html"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	launchdLabel = "build.relic.server"
	launchdPath  = "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
)

// launchd runs the server as the launch agent build.relic.server in the
// user's login session (macOS); it has no system service.
type launchd struct {
	m *machineSetup
}

func (l *launchd) initSystem() string {
	return "launchd"
}

func (l *launchd) tool() string {
	return "launchctl"
}

func (l *launchd) scope() string {
	return "user"
}

func (l *launchd) definition() string {
	return filepath.Join(l.m.home, "Library", "LaunchAgents", launchdLabel+".plist")
}

func (l *launchd) settingsPath() string {
	return filepath.Join(l.m.configDir(), "server.env")
}

func (l *launchd) logPath() string {
	return filepath.Join(l.m.home, "Library", "Logs", "relic.log")
}

func (l *launchd) domain() string {
	return "gui/" + strconv.Itoa(l.m.euid)
}

func (l *launchd) target() string {
	return l.domain() + "/" + launchdLabel
}

func (l *launchd) binary() string {
	plist, err := os.ReadFile(l.definition())
	if err != nil {
		return ""
	}

	_, arguments, ok := strings.Cut(string(plist), "<key>ProgramArguments</key>")
	if !ok {
		return ""
	}

	_, first, ok := strings.Cut(arguments, "<string>")
	if !ok {
		return ""
	}

	binary, _, _ := strings.Cut(first, "</string>")

	return html.UnescapeString(binary)
}

func (l *launchd) installActions(ctx context.Context) ([]machineAction, error) {
	m := l.m
	data := m.dataDir()

	env, err := m.settings(
		"server settings, with secrets",
		l.settingsPath(),
		data,
		filepath.Join(m.configDir(), "tls"),
		0o600,
	)
	if err != nil {
		return nil, err
	}

	agent := m.file(
		"launch agent",
		l.definition(),
		[]byte(launchdPlist(m.executable, l.settingsPath(), l.logPath())),
		0o644,
	)

	actions := []machineAction{
		m.directory("the server's database and files", data, 0o700),
		env,
		agent,
		m.directory("the server's log", filepath.Dir(l.logPath()), 0o755),
		{
			what: "start at login",
			command: []string{
				"launchctl",
				"enable",
				l.target(),
			},
			state: "run",
		},
	}

	properties, loaded := l.properties(ctx)

	switch {
	case !loaded:
		return append(actions, l.loadActions()...), nil
	case agent.state != "unchanged":
		return append(append(actions, l.bootoutAction()), l.loadActions()...), nil
	case env.state != "unchanged" || l.runsAnotherBinary(ctx, properties["pid"], m.executable):
		return append(actions, machineAction{
			what: "restart the server",
			command: []string{
				"launchctl",
				"kickstart",
				"-k",
				l.target(),
			},
			state: "run",
		}), nil
	default:
		return append(actions, machineAction{
			what: "start the server",
			command: []string{
				"launchctl",
				"kickstart",
				l.target(),
			},
			state: "run",
		}), nil
	}
}

// loadActions load the agent and start it: launchd defers RunAtLoad.
func (l *launchd) loadActions() []machineAction {
	return []machineAction{
		{
			what: "load the launch agent",
			command: []string{
				"launchctl",
				"bootstrap",
				l.domain(),
				l.definition(),
			},
			state: "run",
		},
		{
			what: "start the server",
			command: []string{
				"launchctl",
				"kickstart",
				l.target(),
			},
			state: "run",
		},
	}
}

// bootoutAction waits until launchd has removed the agent: bootout returns
// before that, and a bootstrap in between fails.
func (l *launchd) bootoutAction() machineAction {
	return machineAction{
		what: "stop the server",
		command: []string{
			"launchctl",
			"bootout",
			l.target(),
		},
		state: "run",
		done: func(ctx context.Context) bool {
			_, loaded := l.properties(ctx)

			return !loaded
		},
	}
}

// properties are the top-level fields of launchctl print for the agent;
// loaded is false when launchd does not know it.
func (l *launchd) properties(ctx context.Context) (properties map[string]string, loaded bool) {
	out, err := l.m.query(ctx, "launchctl", "print", l.target())
	if err != nil {
		return nil, false
	}

	properties = map[string]string{}

	for line := range strings.Lines(out) {
		field, ok := strings.CutPrefix(line, "\t")
		if !ok || strings.HasPrefix(field, "\t") {
			continue
		}

		if key, value, ok := strings.Cut(strings.TrimSpace(field), " = "); ok {
			properties[key] = value
		}
	}

	return properties, true
}

// runsAnotherBinary reports whether the process started before the binary
// on disk was written.
func (l *launchd) runsAnotherBinary(ctx context.Context, pid, binary string) bool {
	if pid == "" || pid == "0" {
		return false
	}

	out, err := l.m.query(ctx, "ps", "-o", "lstart=", "-p", pid)
	if err != nil {
		return false
	}

	started, err := time.ParseInLocation(time.ANSIC, strings.Join(strings.Fields(out), " "), time.Local)
	if err != nil {
		return false
	}

	onDisk, err := os.Stat(binary)

	return err == nil && onDisk.ModTime().After(started)
}

func launchdPlist(executable, settings, log string) string {
	var b strings.Builder

	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<!-- Written by relic server setup; relic server setup rewrites it. -->
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + launchdLabel + `</string>
	<key>ProgramArguments</key>
	<array>
`)

	for _, argument := range []string{
		executable,
		"server",
		"start",
		"--worker",
		"--settings",
		settings,
	} {
		b.WriteString("\t\t<string>" + xmlEscape(argument) + "</string>\n")
	}

	b.WriteString(`	</array>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PATH</key>
		<string>` + launchdPath + `</string>
	</dict>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>ThrottleInterval</key>
	<integer>3</integer>
	<key>SoftResourceLimits</key>
	<dict>
		<key>NumberOfFiles</key>
		<integer>65536</integer>
	</dict>
	<key>StandardOutPath</key>
	<string>` + xmlEscape(log) + `</string>
	<key>StandardErrorPath</key>
	<string>` + xmlEscape(log) + `</string>
</dict>
</plist>
`)

	return b.String()
}

func (l *launchd) uninstallActions(ctx context.Context) (remove, keep []machineAction) {
	if !exists(l.definition()) {
		return nil, nil
	}

	if _, loaded := l.properties(ctx); loaded {
		remove = append(remove, l.bootoutAction())
	}

	remove = append(remove, removal("launch agent", l.definition()))

	return remove, existing(removal("the server's log", l.logPath()))
}

func (l *launchd) status(ctx context.Context) (serviceState, error) {
	properties, loaded := l.properties(ctx)
	if !loaded {
		return serviceState{
			active:  "inactive",
			detail:  "not loaded",
			enabled: "not loaded",
		}, nil
	}

	state := serviceState{
		active:  "inactive",
		detail:  properties["state"],
		enabled: "enabled",
	}
	state.pid, _ = strconv.Atoi(properties["pid"])

	exit := properties["last exit code"]

	switch {
	case properties["state"] == "running":
		state.active = "active"
	case exit != "" && exit != "0" && !strings.HasPrefix(exit, "("):
		state.active, state.detail = "failed", "exit "+exit
	}

	return state, nil
}

func (l *launchd) stop(ctx context.Context) error {
	bootout := l.bootoutAction()
	if err := l.m.run(ctx, bootout.command[0], bootout.command[1:]...); err != nil {
		return err
	}

	return l.m.settle(ctx, bootout)
}

func (l *launchd) restart(ctx context.Context) error {
	if _, loaded := l.properties(ctx); !loaded {
		for _, a := range l.loadActions() {
			if err := l.m.apply(ctx, a); err != nil {
				return err
			}
		}

		return nil
	}

	return l.m.run(ctx, "launchctl", "kickstart", "-k", l.target())
}

func (l *launchd) startCommand() string {
	return "launchctl bootstrap " + l.domain() + " " + l.m.tilde(l.definition()) +
		" && launchctl kickstart " + l.target()
}

func (l *launchd) statusCommand() string {
	return "launchctl print " + l.target()
}

func (l *launchd) logsCommand() string {
	return "tail -f " + l.m.tilde(l.logPath())
}
