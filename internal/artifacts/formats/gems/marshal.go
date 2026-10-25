package gems

import (
	"bytes"
	"errors"
	"sort"
)

// Symbol is a Ruby symbol.
type Symbol string

// Object is a plain Ruby object with instance variables ("o").
type Object struct {
	Class string
	Ivars []Ivar
}

// Ivar is an instance variable; Name includes the leading "@".
type Ivar struct {
	Name  string
	Value any
}

// UserMarshal is an object dumped through marshal_dump ("U"), such as
// Gem::Version (["1.0.0"]) and Gem::Requirement ([[[op, version]]]).
type UserMarshal struct {
	Class string
	Data  any
}

// UserDefined is an object dumped through _dump ("u"), such as
// Gem::Specification; Data is the string _dump returned.
type UserDefined struct {
	Class string
	Data  []byte
}

// Hash is a Ruby hash with keys in insertion order.
type Hash []KV

// KV is one hash entry.
type KV struct {
	Key, Value any
}

// Marshal encodes v in Marshal 4.8 format.
func Marshal(v any) []byte {
	e := &encoder{
		symbols: map[string]int{},
	}
	e.buf.Write([]byte{
		4,
		8,
	})
	e.value(v)

	return e.buf.Bytes()
}

func marshalAppended(values []any) []byte {
	// No symbol links: they would index the symbol table of the stream these values follow.
	e := &encoder{}
	for _, v := range values {
		e.value(v)
	}

	return e.buf.Bytes()
}

var errMarshalInt = errors.New("marshal: truncated int")

func readInt(b []byte) (int, []byte, error) {
	if len(b) == 0 {
		return 0, nil, errMarshalInt
	}

	c := int(b[0])
	if c > 127 {
		c -= 256
	}

	switch {
	case c == 0:
		return 0, b[1:], nil
	case c > 4:
		return c - 5, b[1:], nil
	case c < -4:
		return c + 5, b[1:], nil
	}

	size := c
	if size < 0 {
		size = -size
	}

	if len(b) < 1+size {
		return 0, nil, errMarshalInt
	}

	n := 0
	for i := size; i >= 1; i-- {
		n = n<<8 | int(b[i])
	}

	if c < 0 {
		n -= 1 << (8 * size)
	}

	return n, b[1+size:], nil
}

type encoder struct {
	buf     bytes.Buffer
	symbols map[string]int
}

func (e *encoder) value(v any) {
	switch x := v.(type) {
	case nil:
		e.buf.WriteByte('0')
	case bool:
		if x {
			e.buf.WriteByte('T')
		} else {
			e.buf.WriteByte('F')
		}
	case int:
		e.buf.WriteByte('i')
		e.int(x)
	case string:
		e.buf.WriteByte('I')
		e.buf.WriteByte('"')
		e.bytes([]byte(x))
		e.int(1)
		e.symbol("E")
		e.buf.WriteByte('T')
	case []byte:
		e.buf.WriteByte('"')
		e.bytes(x)
	case Symbol:
		e.symbol(string(x))
	case []any:
		e.buf.WriteByte('[')
		e.int(len(x))

		for _, el := range x {
			e.value(el)
		}
	case []string:
		e.buf.WriteByte('[')
		e.int(len(x))

		for _, el := range x {
			e.value(el)
		}
	case Hash:
		e.buf.WriteByte('{')
		e.int(len(x))

		for _, kv := range x {
			e.value(kv.Key)
			e.value(kv.Value)
		}
	case map[string]string:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}

		sort.Strings(keys)

		h := make(Hash, 0, len(keys))
		for _, k := range keys {
			h = append(h, KV{
				k,
				x[k],
			})
		}

		e.value(h)
	case Object:
		e.buf.WriteByte('o')
		e.symbol(x.Class)
		e.int(len(x.Ivars))

		for _, iv := range x.Ivars {
			e.symbol(iv.Name)
			e.value(iv.Value)
		}
	case UserMarshal:
		e.buf.WriteByte('U')
		e.symbol(x.Class)
		e.value(x.Data)
	case UserDefined:
		// No encoding ivar: RubyGems' SafeMarshal refuses ivars on
		// Gem::Specification, and Ruby dumps it as a plain "u".
		e.buf.WriteByte('u')
		e.symbol(x.Class)
		e.bytes(x.Data)
	default:
		panic("marshal: unsupported type")
	}
}

func (e *encoder) symbol(s string) {
	if i, ok := e.symbols[s]; ok {
		e.buf.WriteByte(';')
		e.int(i)

		return
	}

	if e.symbols != nil {
		e.symbols[s] = len(e.symbols)
	}

	e.buf.WriteByte(':')
	e.bytes([]byte(s))
}

func (e *encoder) bytes(b []byte) {
	e.int(len(b))
	e.buf.Write(b)
}

func (e *encoder) int(n int) {
	switch {
	case n == 0:
		e.buf.WriteByte(0)
	case n > 0 && n < 123:
		e.buf.WriteByte(byte(n + 5))
	case n < 0 && n > -124:
		e.buf.WriteByte(byte((n - 5) & 0xff))
	default:
		var out []byte

		x := n
		for range 4 {
			out = append(out, byte(x&0xff))

			x >>= 8
			if (n >= 0 && x == 0) || (n < 0 && x == -1) {
				break
			}
		}

		if n >= 0 {
			e.buf.WriteByte(byte(len(out))) //nolint:gosec // 1..4
		} else {
			e.buf.WriteByte(byte(256 - len(out))) //nolint:gosec // 252..255
		}

		e.buf.Write(out)
	}
}
