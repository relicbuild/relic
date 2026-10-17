package relicapi

import (
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
)

func enumPrefix(d protoreflect.EnumDescriptor) string {
	return strings.TrimSuffix(string(d.Values().ByNumber(0).Name()), "UNSPECIFIED")
}

func domainName(e protoreflect.Enum) string {
	d := e.Descriptor()

	v := d.Values().ByNumber(e.Number())
	if v == nil || e.Number() == 0 {
		return ""
	}

	return strings.ReplaceAll(strings.ToLower(strings.TrimPrefix(string(v.Name()), enumPrefix(d))), "_", "")
}

func enumNumber(d protoreflect.EnumDescriptor, name string) (protoreflect.EnumNumber, bool) {
	values := d.Values()
	prefix := enumPrefix(d)

	for i := range values.Len() {
		v := values.Get(i)
		if v.Number() != 0 &&
			strings.ReplaceAll(strings.ToLower(strings.TrimPrefix(string(v.Name()), prefix)), "_", "") == name {
			return v.Number(), true
		}
	}

	return 0, false
}

func toDomain[T ~string](e protoreflect.Enum) (T, bool) {
	n := domainName(e)
	return T(n), n != ""
}

func fromDomain[E interface {
	~int32
	Descriptor() protoreflect.EnumDescriptor
}, T ~string](v T) E {
	var zero E

	n, _ := enumNumber(zero.Descriptor(), string(v))

	return E(n)
}
