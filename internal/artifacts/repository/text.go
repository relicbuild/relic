package repository

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"
)

// storableText reports whether both database engines store s unchanged:
// PostgreSQL refuses invalid UTF-8 and NUL in text, SQLite keeps them.
func storableText(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}

func storableFields(value reflect.Value) bool {
	switch value.Kind() {
	case reflect.String:
		return storableText(value.String())
	case reflect.Pointer, reflect.Interface:
		return value.IsNil() || storableFields(value.Elem())
	case reflect.Struct:
		for i := range value.NumField() {
			if !storableFields(value.Field(i)) {
				return false
			}
		}

		return true
	case reflect.Slice:
		if value.Type() == reflect.TypeFor[json.RawMessage]() {
			return value.Len() == 0 || storableJSON(value.Bytes())
		}

		for i := range value.Len() {
			if !storableFields(value.Index(i)) {
				return false
			}
		}

		return true
	case reflect.Map:
		for k, v := range value.Seq2() {
			if !storableFields(k) || !storableFields(v) {
				return false
			}
		}

		return true
	default:
		return true
	}
}

func storableJSON(doc []byte) bool {
	if !utf8.Valid(doc) {
		return false
	}

	var value any
	if err := json.Unmarshal(doc, &value); err != nil {
		return false
	}

	return storableValue(value)
}

func storableValue(value any) bool {
	switch v := value.(type) {
	case string:
		return storableText(v)
	case []any:
		return !slices.ContainsFunc(v, func(e any) bool {
			return !storableValue(e)
		})
	case map[string]any:
		for k, e := range v {
			if !storableText(k) || !storableValue(e) {
				return false
			}
		}

		return true
	default:
		return true
	}
}
