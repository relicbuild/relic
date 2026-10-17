package relicapi

import (
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"connectrpc.com/vanguard"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Connect registers JSON under two names ("json" and "json; charset=utf-8"); both are replaced.
type strictJSON struct {
	name string
}

func strictJSONCodecs() connect.HandlerOption {
	return connect.WithHandlerOptions(
		connect.WithCodec(strictJSON{
			"json",
		}),
		connect.WithCodec(strictJSON{
			"json; charset=utf-8",
		}),
	)
}

func (c strictJSON) Name() string { return c.name }

func (strictJSON) IsBinary() bool { return false }

func (strictJSON) Marshal(m any) ([]byte, error) {
	msg, ok := m.(proto.Message)
	if !ok {
		return nil, fmt.Errorf("%T is not a protobuf message", m)
	}

	return protojson.Marshal(msg)
}

func (strictJSON) Unmarshal(data []byte, m any) error {
	msg, ok := m.(proto.Message)
	if !ok {
		return fmt.Errorf("%T is not a protobuf message", m)
	}

	if len(data) == 0 {
		return errors.New("zero-length payload is not a valid JSON object")
	}

	if err := protojson.Unmarshal(data, msg); err != nil {
		return fmt.Errorf("unmarshal into %T: %w", m, err)
	}

	return nil
}

func strictRESTJSON(resolver vanguard.TypeResolver) vanguard.Codec {
	codec := vanguard.NewJSONCodec(resolver)
	codec.UnmarshalOptions.DiscardUnknown = false

	return codec
}
