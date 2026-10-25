package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/spf13/cobra"

	relicv1 "relic/api/gen/relic/v1"

	"relic/internal/platform/terminal"
)

type statusResult struct {
	URL       string                 `json:"url"`
	Version   string                 `json:"version"`
	Revision  string                 `json:"revision,omitempty"`
	LatencyMS int64                  `json:"latencyMs"`
	User      string                 `json:"user,omitempty"`
	Admin     bool                   `json:"admin,omitempty"`
	Checks    []*relicv1.HealthCheck `json:"checks,omitempty"`
	Healthy   bool                   `json:"healthy"`
}

func authStatusCmd(g *globals) *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Check the server you are signed in to: reachable, its version, who you are and its health",
		Long: "Calls the server and reports its version, the round trip, the user your credentials sign in as " +
			"and, for administrators, the health checks of its database and object storage. " +
			"Exits with status 1 when a health check fails.",
		Example: "  relic auth status\n  relic auth status -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := serverStatus(cmd, g)
			if err != nil {
				return err
			}

			if err := printMessage(cmd.OutOrStdout(), output, res, func(w io.Writer) error {
				printServerStatus(w, res)

				return nil
			}); err != nil {
				return err
			}

			if !res.Healthy {
				return exitCode1
			}

			return nil
		},
	}
	outputFlag(cmd, &output)

	return cmd
}

func serverStatusCmd(g *globals) *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Check the Relic service on this machine: running, address, settings, sizes and free disk",
		Long: "Reports the state of the service relic server setup installed, whether its address answers and " +
			"with which version, its settings, the sizes of its database, files and scanner database, and the " +
			"free disk. Exits with status 1 when the service is not running or not answering.",
		Example: "  relic server status\n  sudo relic server status   # a system service's settings are root's",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, err := newMachineSetup(cmd, g, g.getenv("HOME"), "/")
			if err != nil {
				return err
			}

			machine := m.machineStatus(cmd.Context())
			if machine == nil {
				return errors.New("no Relic service on this machine: install one with relic server setup")
			}

			if err := printMessage(cmd.OutOrStdout(), output, machine, func(w io.Writer) error {
				m.printMachineStatus(w, machine)

				return nil
			}); err != nil {
				return err
			}

			if !machine.Healthy {
				return exitCode1
			}

			return nil
		},
	}
	outputFlag(cmd, &output)

	return cmd
}

func apiCmd(g *globals) *cobra.Command {
	var (
		method  string
		data    string
		headers []string
		include bool
	)

	cmd := &cobra.Command{
		Use:   "api <path>",
		Short: "Call the Relic API with your credentials",
		Long: "Sends an HTTP request to the server, signed in as the current context, and prints the response body. " +
			"A path without a leading / is under /api/v1/ (see relic api docs); JSON responses are indented. " +
			"--data sends a body (@file reads a file, @- standard input) and makes the default method POST. " +
			"Exits with status 1 when the response is an error (4xx or 5xx).",
		Example: "  relic api version\n" +
			"  relic api repositories\n" +
			"  relic api -X POST 'builds/app/runs/7:promote' -d '{\"targetRepository\": \"libs-release\"}'\n" +
			"  relic api /api/relic.v1.SystemService/GetVersion -d '{}'",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeOperations(g),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.dial()
			if err != nil {
				return err
			}

			body, err := requestBody(cmd, data)
			if err != nil {
				return err
			}

			if method == "" {
				method = http.MethodGet
				if body != nil {
					method = http.MethodPost
				}
			}

			var rd io.Reader
			if body != nil {
				rd = bytes.NewReader(body)
			}

			req, err := http.NewRequestWithContext(cmd.Context(), strings.ToUpper(method), c.url+apiPath(args[0]), rd)
			if err != nil {
				return err
			}

			if body != nil {
				req.Header.Set("Content-Type", "application/json")
			}

			given := map[string]bool{}

			for _, h := range headers {
				k, v, ok := strings.Cut(h, ":")
				if !ok {
					return fmt.Errorf("header %q is not name: value", h)
				}

				k = http.CanonicalHeaderKey(strings.TrimSpace(k))
				if !given[k] {
					req.Header.Del(k)
					given[k] = true
				}

				req.Header.Add(k, strings.TrimSpace(v))
			}

			resp, err := c.http.Do(req)
			if err != nil {
				return err
			}

			defer func() {
				_ = resp.Body.Close()
			}()

			out := cmd.OutOrStdout()
			if include {
				_, _ = fmt.Fprintf(out, "%s %s\n", resp.Proto, resp.Status)
				_ = resp.Header.Write(out)
				_, _ = fmt.Fprintln(out)
			}

			if err := writeBody(out, resp); err != nil {
				return err
			}

			if resp.StatusCode >= http.StatusBadRequest {
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), resp.Status)

				return exitCode1
			}

			return nil
		},
	}
	cmd.Flags().StringVarP(&method, "method", "X", "", "HTTP method (default GET, or POST with --data)")
	cmd.Flags().StringVarP(&data, "data", "d", "", "request body: JSON, @file or @- for standard input")
	cmd.Flags().StringArrayVarP(&headers, "header", "H", nil, "extra header, name: value (repeatable)")
	cmd.Flags().BoolVarP(&include, "include", "i", false, "print the status line and headers too")
	_ = cmd.RegisterFlagCompletionFunc(
		"method",
		cobra.FixedCompletions(
			[]string{
				http.MethodGet,
				http.MethodPost,
				http.MethodPut,
				http.MethodPatch,
				http.MethodDelete,
				http.MethodHead,
			},
			cobra.ShellCompDirectiveNoFileComp,
		),
	)
	cmd.AddCommand(apiDocsCmd(g))

	return cmd
}

func apiPath(p string) string {
	if strings.HasPrefix(p, "/") {
		return p
	}

	return "/api/v1/" + p
}

func requestBody(cmd *cobra.Command, data string) ([]byte, error) {
	switch {
	case data == "":
		return nil, nil
	case data == "@-":
		return io.ReadAll(cmd.InOrStdin())
	case strings.HasPrefix(data, "@"):
		return os.ReadFile(data[1:])
	default:
		return []byte(data), nil
	}
}

func writeBody(w io.Writer, resp *http.Response) error {
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		_, err := io.Copy(w, resp.Body)

		return err
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var out bytes.Buffer
	if json.Indent(&out, body, "", "  ") == nil {
		out.WriteByte('\n')
		_, err := w.Write(out.Bytes())

		return err
	}

	_, err = w.Write(body)

	return err
}

type operation struct {
	ID          string          `json:"operationId"`
	Method      string          `json:"method"`
	Path        string          `json:"path"`
	Tags        []string        `json:"tags,omitempty"`
	Summary     string          `json:"summary,omitempty"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	RequestBody json.RawMessage `json:"requestBody,omitempty"`
	Responses   json.RawMessage `json:"responses,omitempty"`
}

type openAPI struct {
	Paths      map[string]map[string]json.RawMessage `json:"paths"`
	Components struct {
		Schemas map[string]json.RawMessage `json:"schemas"`
	} `json:"components"`
}

func (c *client) operations(cmd *cobra.Command) ([]operation, openAPI, error) {
	var doc openAPI

	req, err := http.NewRequestWithContext(cmd.Context(), http.MethodGet, c.url+"/api/v1/openapi.json", nil)
	if err != nil {
		return nil, doc, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, doc, err
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, doc, fmt.Errorf("api docs: %w", httpError(resp))
	}

	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, doc, fmt.Errorf("api docs: %w", err)
	}

	var ops []operation

	for p, item := range doc.Paths {
		for m, raw := range item {
			if m == "parameters" {
				continue
			}

			op := operation{
				Method: strings.ToUpper(m),
				Path:   p,
			}
			if err := json.Unmarshal(raw, &op); err != nil {
				return nil, doc, fmt.Errorf("api docs: %s %s: %w", m, p, err)
			}

			if op.ID == "" {
				op.ID = op.Method + " " + p
			}

			if shared, ok := item["parameters"]; ok && op.Parameters == nil {
				op.Parameters = shared
			}

			ops = append(ops, op)
		}
	}

	slices.SortFunc(ops, func(a, b operation) int {
		return strings.Compare(a.Path+" "+a.Method, b.Path+" "+b.Method)
	})

	return ops, doc, nil
}

func (o operation) matches(query string) bool {
	q := strings.ToLower(query)

	return strings.Contains(strings.ToLower(strings.Join([]string{
		o.ID,
		o.Method,
		o.Path,
		o.Summary,
		o.Description,
		strings.Join(o.Tags, " "),
	}, " ")), q)
}

func completeOperations(g *globals) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		c, err := g.dial()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		ops, _, err := c.operations(cmd)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		var out []string

		for _, o := range ops {
			if o.Method == http.MethodGet && !strings.Contains(o.Path, "{") {
				out = append(out, strings.TrimPrefix(o.Path, "/api/v1/")+"\t"+o.Summary)
			}
		}

		return slices.Compact(out), cobra.ShellCompDirectiveNoFileComp
	}
}

func apiDocsCmd(g *globals) *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:   "docs [<query>]",
		Short: "List the Relic API's operations, or those matching a query",
		Long: "Reads the server's own OpenAPI description, so it always matches the server you use. " +
			"The query matches operation IDs, paths, summaries, descriptions and tags.",
		Example: "  relic api docs\n  relic api docs token\n  relic api docs describe SecurityService_CreateToken",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.dial()
			if err != nil {
				return err
			}

			ops, _, err := c.operations(cmd)
			if err != nil {
				return err
			}

			if len(args) == 1 {
				ops = slices.DeleteFunc(ops, func(o operation) bool {
					return !o.matches(args[0])
				})
			}

			if len(ops) == 0 {
				return errors.New("api docs: no operation matches")
			}

			type summary struct {
				ID      string `json:"operationId"`
				Method  string `json:"method"`
				Path    string `json:"path"`
				Summary string `json:"summary,omitempty"`
			}

			list := make([]summary, 0, len(ops))
			for _, o := range ops {
				list = append(list, summary{
					ID:      o.ID,
					Method:  o.Method,
					Path:    o.Path,
					Summary: o.Summary,
				})
			}

			return printMessage(cmd.OutOrStdout(), output, list, func(w io.Writer) error {
				table := terminal.Table{
					Headers: []string{
						"METHOD",
						"PATH",
						"SUMMARY",
					},
				}

				for _, o := range list {
					table.Rows = append(table.Rows, []string{
						o.Method,
						o.Path,
						tableCell(o.Summary),
					})
				}

				return table.Print(w)
			})
		},
	}
	outputFlag(cmd, &output)
	cmd.AddCommand(apiDescribeCmd(g))

	return cmd
}

func apiDescribeCmd(g *globals) *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:   "describe <operation>",
		Short: "Describe one operation: parameters, request and response, with their schemas",
		Long: "Names an operation by its ID (SecurityService_CreateToken) or by method and path " +
			"(\"POST /api/v1/tokens\"). The output includes the schemas the operation refers to.",
		Example: "  relic api docs describe SecurityService_CreateToken\n  relic api docs describe 'GET /api/v1/version' -o json",
		Args:    cobra.ExactArgs(1),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
			c, err := g.dial()
			if err != nil || len(args) > 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}

			ops, _, err := c.operations(cmd)
			if err != nil {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}

			var out []string
			for _, o := range ops {
				out = append(out, o.ID+"\t"+o.Summary)
			}

			return out, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.dial()
			if err != nil {
				return err
			}

			ops, doc, err := c.operations(cmd)
			if err != nil {
				return err
			}

			i := slices.IndexFunc(ops, func(o operation) bool {
				return o.ID == args[0] || strings.EqualFold(o.Method+" "+o.Path, args[0])
			})
			if i < 0 {
				return fmt.Errorf("api docs describe: no operation %q (relic api docs lists them)", args[0])
			}

			op := ops[i]

			type described struct {
				operation

				Schemas map[string]json.RawMessage `json:"schemas,omitempty"`
			}

			d := described{
				operation: op,
				Schemas:   referencedSchemas(doc, op),
			}

			return printMessage(cmd.OutOrStdout(), output, d, func(w io.Writer) error {
				_, _ = fmt.Fprintf(w, "%s %s\n%s\n", op.Method, op.Path, op.ID)
				if op.Summary != "" {
					_, _ = fmt.Fprintf(w, "\n%s\n", op.Summary)
				}

				if op.Description != "" {
					_, _ = fmt.Fprintf(w, "\n%s\n", op.Description)
				}

				for _, part := range []struct {
					name string
					raw  json.RawMessage
				}{
					{
						name: "Parameters",
						raw:  op.Parameters,
					},
					{
						name: "Request body",
						raw:  op.RequestBody,
					},
					{
						name: "Responses",
						raw:  op.Responses,
					},
				} {
					if len(part.raw) == 0 {
						continue
					}

					var b bytes.Buffer
					if err := json.Indent(&b, part.raw, "  ", "  "); err != nil {
						return err
					}

					_, _ = fmt.Fprintf(w, "\n%s:\n  %s\n", part.name, b.String())
				}

				for _, name := range slices.Sorted(func(yield func(string) bool) {
					for k := range d.Schemas {
						if !yield(k) {
							return
						}
					}
				}) {
					var b bytes.Buffer
					if err := json.Indent(&b, d.Schemas[name], "  ", "  "); err != nil {
						return err
					}

					_, _ = fmt.Fprintf(w, "\nSchema %s:\n  %s\n", name, b.String())
				}

				return nil
			})
		},
	}
	outputFlag(cmd, &output)

	return cmd
}

func referencedSchemas(doc openAPI, op operation) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}

	var walk func(v any)

	walk = func(v any) {
		switch v := v.(type) {
		case []any:
			for _, e := range v {
				walk(e)
			}
		case map[string]any:
			for k, e := range v {
				ref, _ := e.(string)
				name, ok := strings.CutPrefix(ref, "#/components/schemas/")

				if k != "$ref" || !ok {
					walk(e)

					continue
				}

				if _, seen := out[name]; seen {
					continue
				}

				if s, ok := doc.Components.Schemas[name]; ok {
					out[name] = s

					var schema any
					if json.Unmarshal(s, &schema) == nil {
						walk(schema)
					}
				}
			}
		}
	}

	for _, raw := range []json.RawMessage{
		op.Parameters,
		op.RequestBody,
		op.Responses,
	} {
		var v any
		if json.Unmarshal(raw, &v) == nil {
			walk(v)
		}
	}

	return out
}

func serverStatus(cmd *cobra.Command, g *globals) (*statusResult, error) {
	c, err := g.dial()
	if err != nil {
		return nil, err
	}

	start := time.Now()

	v, err := c.system.GetVersion(cmd.Context(), &relicv1.GetVersionRequest{})
	if err != nil {
		return nil, fmt.Errorf("status: %s: %w", c.url, err)
	}

	res := &statusResult{
		URL:       c.url,
		Version:   v.GetVersion(),
		Revision:  v.GetRevision(),
		LatencyMS: time.Since(start).Milliseconds(),
		Healthy:   true,
	}

	me, err := c.sessions.GetCurrentUser(cmd.Context(), &relicv1.GetCurrentUserRequest{})
	switch {
	case err == nil:
		res.User = me.GetPrincipal().GetUsername()
		res.Admin = me.GetPrincipal().GetAdmin()
	case connect.CodeOf(err) == connect.CodeUnavailable:
		res.Healthy = false
		res.Checks = append(res.Checks, unavailableCheck(err))

		return res, nil
	case connect.CodeOf(err) != connect.CodeUnauthenticated:
		return nil, fmt.Errorf("status: %w", err)
	}

	if res.Admin {
		st, err := c.system.GetSystemStatus(
			cmd.Context(),
			&relicv1.GetSystemStatusRequest{},
		)
		if err != nil {
			return nil, fmt.Errorf("status: %w", err)
		}

		res.Checks = st.GetChecks()
		for _, ch := range res.Checks {
			res.Healthy = res.Healthy && ch.GetOk()
		}
	}

	return res, nil
}

func unavailableCheck(err error) *relicv1.HealthCheck {
	message := err.Error()
	if ce, ok := errors.AsType[*connect.Error](err); ok {
		message = ce.Message()
	}

	name, ok := strings.CutSuffix(message, " unavailable")
	if !ok || name == "" {
		name = "server"
	}

	return &relicv1.HealthCheck{
		Name:  name,
		Error: message,
	}
}

func printServerStatus(w io.Writer, res *statusResult) {
	user := "anonymous"
	if res.User != "" {
		user = res.User
	}

	if res.Admin {
		user += styleMuted.Render(" (administrator)")
	}

	rows := make([]panelRow, 0, 3+len(res.Checks))
	rows = append(rows, []panelRow{
		{
			label: "version",
			value: styleValue.Render("Relic " + res.Version),
		},
		{
			label: "latency",
			value: styleValue.Render(fmt.Sprintf("%d ms", res.LatencyMS)),
		},
		{
			label: "signed in",
			value: styleValue.Render(user),
		},
	}...)

	for _, ch := range res.Checks {
		rows = append(rows, panelRow{
			label: ch.GetName(),
			value: check(
				ch.GetOk(),
				"ok",
				ch.GetError(),
			) + styleMuted.Render(
				fmt.Sprintf("  %d ms", ch.GetLatencyMs()),
			),
		})
	}

	printStyled(w, panel("Server", res.URL, rows))
}
