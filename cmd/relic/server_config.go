package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"relic/internal/app"
)

const maskedValue = "********"

var (
	secretSetting   = regexp.MustCompile(`(KEY|PASSWORD|SECRET|TOKEN|CONNECTION_STRING|HEADERS)$`)
	secretParameter = regexp.MustCompile(`(?i)(key|password|secret|token)$`)
)

func serverConfigCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show the settings of the Relic service on this machine, secrets masked",
		Long: "Prints the settings file relic server setup wrote (server.env, or /etc/relic/relic.env for a system " +
			"service), with keys, passwords, secrets, tokens, connection strings, headers and the passwords in " +
			"URLs and PostgreSQL connection strings masked. relic server config set changes one setting and restarts the service.",
		Example: "  relic server config\n" +
			"  relic server config set RELIC_HTTP_LISTEN_ADDRESS=0.0.0.0:8090",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, _, err := serviceSettings(cmd, g)
			if err != nil {
				return err
			}

			b, err := os.ReadFile(path) //nolint:gosec // the settings file relic server setup wrote
			if err != nil {
				return fmt.Errorf("read %s: %w (a system service's settings need sudo)", path, err)
			}

			for l := range strings.Lines(string(b)) {
				_, _ = fmt.Fprint(cmd.OutOrStdout(), maskedSettingsLine(l))
			}

			return nil
		},
	}
	cmd.AddCommand(serverConfigSetCmd(g))

	return cmd
}

func maskedSettingsLine(line string) string {
	key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
	if !ok || strings.HasPrefix(key, "#") {
		return line
	}

	if secretSetting.MatchString(key) {
		return key + "=" + maskedValue + "\n"
	}

	if masked := withoutURLCredentials(value); masked != value {
		return key + "=" + masked + "\n"
	}

	if masked := withoutConnectionStringSecrets(value); masked != value {
		return key + "=" + masked + "\n"
	}

	return line
}

// connectionParameter is one name=value field of a keyword/value connection
// string, with the byte offsets of its value as written, quotes included.
type connectionParameter struct {
	name       string
	value      string
	valueStart int
	valueEnd   int
}

// connectionParameters parses a keyword/value connection string such as
// "host=db password='a b'" the way PostgreSQL clients do, returning nil when
// the text is not one.
func connectionParameters(text string) []connectionParameter {
	var parameters []connectionParameter

	i := skipSpaces(text, 0)
	for i < len(text) {
		nameStart := i
		for i < len(text) && text[i] != '=' && !isSpace(text[i]) {
			i++
		}

		name := text[nameStart:i]

		i = skipSpaces(text, i)
		if name == "" || i == len(text) || text[i] != '=' {
			return nil
		}

		valueStart := skipSpaces(text, i+1)

		value, valueEnd, ok := connectionValue(text, valueStart)
		if !ok {
			return nil
		}

		parameters = append(parameters, connectionParameter{
			name:       name,
			value:      value,
			valueStart: valueStart,
			valueEnd:   valueEnd,
		})
		i = skipSpaces(text, valueEnd)
	}

	return parameters
}

func connectionValue(text string, start int) (string, int, bool) {
	quoted := start < len(text) && text[start] == '\''
	if quoted {
		start++
	}

	var value strings.Builder

	for i := start; i < len(text); i++ {
		switch c := text[i]; {
		case c == '\\' && i+1 < len(text):
			i++
			value.WriteByte(text[i])
		case quoted && c == '\'':
			return value.String(), i + 1, true
		case !quoted && isSpace(c):
			return value.String(), i, true
		default:
			value.WriteByte(c)
		}
	}

	return value.String(), len(text), !quoted
}

func skipSpaces(text string, i int) int {
	for i < len(text) && isSpace(text[i]) {
		i++
	}

	return i
}

func isSpace(c byte) bool {
	return strings.IndexByte(" \t\n\r\f\v", c) >= 0
}

func withoutConnectionStringSecrets(value string) string {
	var masked strings.Builder

	written := 0

	for _, p := range connectionParameters(value) {
		if secretParameter.MatchString(p.name) {
			masked.WriteString(value[written:p.valueStart])
			masked.WriteString(maskedValue)

			written = p.valueEnd
		}
	}

	if written == 0 {
		return value
	}

	masked.WriteString(value[written:])

	return masked.String()
}

func withoutURLCredentials(value string) string {
	u, err := url.Parse(value)
	if err != nil || u.Scheme == "" {
		return value
	}

	masked := false

	parameters := strings.Split(u.RawQuery, "&")
	for i, p := range parameters {
		if name, _, ok := strings.Cut(p, "="); ok && secretParameter.MatchString(name) {
			parameters[i], masked = name+"="+maskedValue, true
		}
	}

	u.RawQuery = strings.Join(parameters, "&")

	if _, ok := u.User.Password(); ok {
		u.User = url.User(u.User.Username())

		return strings.Replace(u.String(), "@", ":"+maskedValue+"@", 1)
	}

	if !masked {
		return value
	}

	return u.String()
}

func serverConfigSetCmd(g *globals) *cobra.Command {
	var valueStdin bool

	cmd := &cobra.Command{
		Use:   "set <KEY>=<value>",
		Short: "Change one setting of the Relic service on this machine and restart it",
		Long: "Writes the setting and restarts the service, also when the setting already has that value, " +
			"so the service runs with it. --value-stdin reads the value from standard input, which keeps " +
			"a secret out of the process list and the shell history.",
		Example: "  relic server config set RELIC_LOG_LEVEL=debug\n" +
			"  printf '%s' \"$PASSWORD\" | relic server config set RELIC_ADMIN_PASSWORD --value-stdin",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, value, err := settingArgument(args[0], valueStdin, cmd.InOrStdin())
			if err != nil {
				return err
			}

			path, m, err := serviceSettings(cmd, g)
			if err != nil {
				return err
			}

			return m.setServiceSetting(cmd.Context(), cmd.OutOrStdout(), path, key, value)
		},
	}
	cmd.Flags().
		BoolVar(&valueStdin, "value-stdin", false, "read the value from standard input; the argument is the key")

	return cmd
}

func settingArgument(arg string, valueStdin bool, in io.Reader) (string, string, error) {
	key, value, ok := strings.Cut(arg, "=")

	if valueStdin {
		if ok {
			return "", "", errors.New("with --value-stdin, give the key alone")
		}

		line, err := bufio.NewReader(in).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", "", fmt.Errorf("read the value: %w", err)
		}

		value, ok = strings.TrimRight(line, "\r\n"), true
	}

	if !ok || (!strings.HasPrefix(key, "RELIC_") && !strings.HasPrefix(key, "OTEL_")) {
		return "", "", errors.New(
			"expected RELIC_<NAME>=<value> or OTEL_<NAME>=<value>, or the name alone with --value-stdin",
		)
	}

	if strings.HasPrefix(key, "RELIC_") && !slices.Contains(app.SettingNames, key) {
		return "", "", fmt.Errorf("%s is not a server setting; the configuration reference lists them", key)
	}

	return key, value, nil
}

// newSettingsErrors returns the errors the server would start with after
// the settings change from before to after, leaving out the ones it already had.
func newSettingsErrors(before, after string) error {
	had := map[string]bool{}
	for _, line := range settingsErrors(before) {
		had[line] = true
	}

	var errs []error

	for _, line := range settingsErrors(after) {
		if !had[line] {
			errs = append(errs, errors.New(line))
		}
	}

	return errors.Join(errs...)
}

func settingsErrors(settings string) []string {
	env := keyValues(settings, "=")

	err := app.CheckSettings(func(key string) string {
		return env[key]
	}, "server")
	if err == nil {
		return nil
	}

	var lines []string

	for line := range strings.Lines(err.Error()) {
		line = strings.TrimSuffix(line, "\n")
		line = strings.TrimPrefix(line, "telemetry: ")
		lines = append(lines, strings.TrimPrefix(line, "config: "))
	}

	return lines
}

func (m *machineSetup) setServiceSetting(ctx context.Context, w io.Writer, path, key, value string) error {
	b, err := os.ReadFile(path) //nolint:gosec // the settings file relic server setup wrote
	if err != nil {
		return fmt.Errorf("read %s: %w (a system service's settings need sudo)", path, err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}

	svc, err := m.serviceManager()
	if err != nil {
		return err
	}

	if err := m.findCommands([]machineAction{
		{
			command: []string{
				svc.tool(),
			},
		},
	}); err != nil {
		return err
	}

	settings := withSetting(string(b), key, value, true)
	if err := newSettingsErrors(string(b), settings); err != nil {
		return fmt.Errorf("the server would not start with this %s; nothing was changed:\n%w", key, err)
	}

	a := m.file("server settings", path, []byte(settings), fi.Mode().Perm())

	done := "Set " + key + " and restarted the service."
	if a.state == "unchanged" {
		done = key + " is already set to that; restarted the service."
	} else if err := m.apply(ctx, a); err != nil {
		return err
	}

	if err := svc.restart(ctx); err != nil {
		return fmt.Errorf("restart the service: %w", err)
	}

	_, _ = fmt.Fprintln(w, done)

	return nil
}

func serviceSettings(cmd *cobra.Command, g *globals) (string, *machineSetup, error) {
	m, err := newMachineSetup(cmd, g, g.getenv("HOME"), "/")
	if err != nil {
		return "", nil, err
	}

	if svc := m.installedService(); svc != nil {
		return svc.settingsPath(), m, nil
	}

	return "", nil, errors.New("no Relic service on this machine: install one with relic server setup")
}
