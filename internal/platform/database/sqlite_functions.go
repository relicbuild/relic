package database

import (
	"database/sql/driver"
	"fmt"
	"regexp"
	"sync"

	"modernc.org/sqlite"
)

var regexps sync.Map

func init() {
	sqlite.MustRegisterDeterministicScalarFunction(
		"regexp",
		2,
		func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			pattern, ok := args[0].(string)
			if !ok {
				return nil, fmt.Errorf("regexp: the pattern is a %T, not text", args[0])
			}

			var text string

			switch v := args[1].(type) {
			case nil:
				return nil, nil
			case string:
				text = v
			case []byte:
				text = string(v)
			default:
				text = fmt.Sprint(v)
			}

			re, ok := regexps.Load(pattern)
			if !ok {
				compiled, err := regexp.Compile(pattern)
				if err != nil {
					return nil, fmt.Errorf("regexp %q: %w", pattern, err)
				}

				re, _ = regexps.LoadOrStore(pattern, compiled)
			}

			return re.(*regexp.Regexp).MatchString(
				text,
			), nil //nolint:forcetypeassert // the map holds compiled patterns only
		},
	)
}
