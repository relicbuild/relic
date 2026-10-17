package policy

import (
	"cmp"
	"fmt"
	"sync"

	"cel.dev/cel-go/cel"

	"relic/internal/scanning/scan"
)

const conditionCost = 10000

var (
	conditionEnv  *cel.Env
	conditionOnce sync.Once
	conditionErr  error
	programs      sync.Map
)

func environment() (*cel.Env, error) {
	conditionOnce.Do(func() {
		conditionEnv, conditionErr = cel.NewEnv(
			cel.Variable("id", cel.StringType),
			cel.Variable("cves", cel.ListType(cel.StringType)),
			cel.Variable("severity", cel.StringType),
			cel.Variable("cvss", cel.DoubleType),
			cel.Variable("fixable", cel.BoolType),
			cel.Variable("malicious", cel.BoolType),
			cel.Variable("package_name", cel.StringType),
			cel.Variable("version", cel.StringType),
			cel.Variable("purl", cel.StringType),
			cel.Variable("analyzer", cel.StringType),
			cel.Variable("rule", cel.StringType),
			cel.Variable("file", cel.StringType),
			cel.Variable("repository", cel.StringType),
			cel.Variable("path", cel.StringType),
			cel.Variable("build", cel.StringType),
			cel.Variable("eol", cel.BoolType),
			cel.Variable("unmaintained", cel.BoolType),
			cel.Variable("reachable", cel.StringType),
			cel.Variable("secret_live", cel.BoolType),
		)
	})

	return conditionEnv, conditionErr
}

func compileCondition(expr string) (cel.Program, error) {
	if p, ok := programs.Load(expr); ok {
		return p.(cel.Program), nil
	}

	env, err := environment()
	if err != nil {
		return nil, err
	}

	ast, iss := env.Compile(expr)
	if iss.Err() != nil {
		return nil, fmt.Errorf("%w: condition: %w", ErrInvalid, iss.Err())
	}

	if ast.OutputType() != cel.BoolType {
		return nil, fmt.Errorf("%w: condition must be true or false, not %s", ErrInvalid, ast.OutputType())
	}

	prg, err := env.Program(ast, cel.CostLimit(conditionCost))
	if err != nil {
		return nil, fmt.Errorf("%w: condition: %w", ErrInvalid, err)
	}

	programs.Store(expr, prg)

	return prg, nil
}

// holds reports whether is meets the condition expr; one that cannot be
// compiled or evaluated holds, so the rule it narrows fails closed.
func holds(expr string, is Issue) bool {
	if expr == "" {
		return true
	}

	prg, err := compileCondition(expr)
	if err != nil {
		return true
	}

	out, _, err := prg.Eval(map[string]any{
		"id":           is.ID,
		"cves":         nonNilStrings(is.CVEs),
		"severity":     string(is.Severity),
		"cvss":         is.CVSS,
		"fixable":      is.Fixable,
		"malicious":    is.Malicious,
		"package_name": is.Package,
		"version":      is.Version,
		"purl":         is.PURL,
		"analyzer":     is.Analyzer,
		"rule":         is.Rule,
		"file":         is.File,
		"repository":   is.Repository,
		"path":         is.Path,
		"build":        is.Build,
		"eol":          is.Risk == RiskEndOfLife,
		"unmaintained": is.Risk == RiskDeprecated || is.Risk == RiskUnmaintained,
		"reachable":    cmp.Or(is.Reachability, scan.ReachabilityUnknown),
		"secret_live":  is.SecretLive,
	})
	if err != nil {
		return true
	}

	b, ok := out.Value().(bool)

	return !ok || b
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}

	return s
}
