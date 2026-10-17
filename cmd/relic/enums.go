package main

import (
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
)

func expandEnums(spec map[string]any, md protoreflect.MessageDescriptor) {
	fields := md.Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)
		key := fd.JSONName()

		v, ok := spec[key]
		if !ok {
			key = string(fd.Name())
			v, ok = spec[key]
		}

		if !ok {
			continue
		}

		switch {
		case fd.IsMap():
			if fd.MapValue().Kind() == protoreflect.MessageKind {
				if m, ok := v.(map[string]any); ok {
					for _, e := range m {
						if em, ok := e.(map[string]any); ok {
							expandEnums(em, fd.MapValue().Message())
						}
					}
				}
			}
		case fd.Kind() == protoreflect.EnumKind:
			if l, ok := v.([]any); ok {
				for j, e := range l {
					l[j] = expandEnum(e, fd.Enum())
				}
			} else {
				spec[key] = expandEnum(v, fd.Enum())
			}
		case fd.Kind() == protoreflect.MessageKind:
			switch m := v.(type) {
			case map[string]any:
				expandEnums(m, fd.Message())
			case []any:
				for _, e := range m {
					if em, ok := e.(map[string]any); ok {
						expandEnums(em, fd.Message())
					}
				}
			}
		}
	}
}

func expandEnum(v any, ed protoreflect.EnumDescriptor) any {
	s, ok := v.(string)
	if !ok || ed.Values().ByName(protoreflect.Name(s)) != nil {
		return v
	}

	prefix := strings.TrimSuffix(string(ed.Values().ByNumber(0).Name()), "UNSPECIFIED")
	want := alnum(s)

	values := ed.Values()
	for i := range values.Len() {
		name := string(values.Get(i).Name())
		if alnum(strings.TrimPrefix(name, prefix)) == want {
			return name
		}
	}

	return v
}

func alnum(s string) string {
	var b strings.Builder

	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}

	return b.String()
}
