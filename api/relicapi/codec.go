package relicapi

import (
	"context"
	"errors"
	"fmt"
	"io"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connectproto"
	"connectrpc.com/vanguard"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// strictJSON is Connect's JSON codec refusing empty payloads and unknown fields.
type strictJSON struct{}

func (strictJSON) Name() string { return connect.CodecNameJSON }

func (strictJSON) MarshalWrite(_ context.Context, dst io.Writer, m any) error {
	msg, ok := m.(proto.Message)
	if !ok {
		return fmt.Errorf("%T is not a protobuf message", m)
	}

	data, err := protojson.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal %T: %w", m, err)
	}

	if _, err := dst.Write(data); err != nil {
		return fmt.Errorf("write %T: %w", m, err)
	}

	return nil
}

func (strictJSON) UnmarshalRead(_ context.Context, src io.Reader, m any) error {
	msg, ok := m.(proto.Message)
	if !ok {
		return fmt.Errorf("%T is not a protobuf message", m)
	}

	data, err := io.ReadAll(src)
	if err != nil {
		return fmt.Errorf("read %T: %w", m, err)
	}

	if len(data) == 0 {
		return errors.New("zero-length payload is not a valid JSON object")
	}

	if err := protojson.Unmarshal(data, msg); err != nil {
		return fmt.Errorf("unmarshal into %T: %w", m, err)
	}

	return nil
}

func strictRESTJSON(resolver connectproto.TypeResolver) vanguard.RESTCodec {
	codec := vanguard.NewJSONCodec(resolver)
	codec.UnmarshalOptions.DiscardUnknown = false

	return codec
}
