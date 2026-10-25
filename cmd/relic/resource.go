package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type document struct {
	kind  *kind
	name  string
	msg   proto.Message
	owned map[string]bool
}

func (d document) String() string { return strings.ToLower(d.kind.name) + "/" + d.name }

type resourceYAML struct {
	APIVersion string       `yaml:"apiVersion"`
	Kind       string       `yaml:"kind"`
	Metadata   metadataYAML `yaml:"metadata"`
	Spec       specYAML     `yaml:"spec,omitempty"`
}

// specYAML is a document's spec, its timestamps kept as the text written.
type specYAML map[string]any

func (s *specYAML) UnmarshalYAML(n *yaml.Node) error {
	v, err := plainValue(n)
	if err != nil {
		return err
	}

	m, ok := v.(map[string]any)
	if v != nil && !ok {
		return fmt.Errorf("line %d: spec is not a mapping", n.Line)
	}

	*s = m

	return nil
}

func plainValue(n *yaml.Node) (any, error) {
	return plainNode(n, map[*yaml.Node]bool{})
}

func plainNode(n *yaml.Node, expanding map[*yaml.Node]bool) (any, error) {
	switch n.Kind {
	case yaml.AliasNode:
		if expanding[n.Alias] {
			return nil, fmt.Errorf("line %d: anchor %q contains itself", n.Line, n.Value)
		}

		expanding[n.Alias] = true
		defer delete(expanding, n.Alias)

		return plainNode(n.Alias, expanding)
	case yaml.MappingNode:
		return plainMapping(n, expanding)
	case yaml.SequenceNode:
		out := make([]any, 0, len(n.Content))
		for _, c := range n.Content {
			v, err := plainNode(c, expanding)
			if err != nil {
				return nil, err
			}

			out = append(out, v)
		}

		return out, nil
	case yaml.ScalarNode:
		if n.ShortTag() == "!!timestamp" {
			return n.Value, nil
		}
	case yaml.DocumentNode:
	}

	var v any
	if err := n.Decode(&v); err != nil {
		return nil, err
	}

	return v, nil
}

func plainMapping(n *yaml.Node, expanding map[*yaml.Node]bool) (map[string]any, error) {
	out := make(map[string]any, len(n.Content)/2)
	lines := make(map[string]int, len(n.Content)/2)

	var merged []*yaml.Node

	for i := 0; i+1 < len(n.Content); i += 2 {
		key, value := n.Content[i], n.Content[i+1]
		if key.Kind == yaml.ScalarNode && key.ShortTag() == "!!merge" {
			merged = append(merged, value)

			continue
		}

		if line, ok := lines[key.Value]; ok {
			return nil, fmt.Errorf("line %d: mapping key %q already defined at line %d", key.Line, key.Value, line)
		}

		lines[key.Value] = key.Line

		v, err := plainNode(value, expanding)
		if err != nil {
			return nil, err
		}

		out[key.Value] = v
	}

	for _, m := range merged {
		if err := mergeMappings(out, m, expanding); err != nil {
			return nil, err
		}
	}

	return out, nil
}

func mergeMappings(out map[string]any, n *yaml.Node, expanding map[*yaml.Node]bool) error {
	sources := []*yaml.Node{
		n,
	}
	if target := aliasTarget(n); target.Kind == yaml.SequenceNode {
		sources = target.Content
	}

	for _, source := range sources {
		if aliasTarget(source).Kind != yaml.MappingNode {
			return fmt.Errorf("line %d: a merge key takes a mapping or a list of mappings", source.Line)
		}

		v, err := plainNode(source, expanding)
		if err != nil {
			return err
		}

		for key, value := range v.(map[string]any) {
			if _, set := out[key]; !set {
				out[key] = value
			}
		}
	}

	return nil
}

func aliasTarget(n *yaml.Node) *yaml.Node {
	for n.Kind == yaml.AliasNode {
		n = n.Alias
	}

	return n
}

type metadataYAML struct {
	Name string `yaml:"name"`
}

func readDocuments(files []string, stdin io.Reader) ([]document, error) {
	if len(files) == 0 {
		return nil, errors.New("give at least one file with -f")
	}

	var out []document

	for _, name := range files {
		paths, err := documentFiles(name)
		if err != nil {
			return nil, err
		}

		for _, f := range paths {
			r := stdin

			if f != "-" {
				b, err := os.ReadFile(f) //nolint:gosec // the user names the files to read
				if err != nil {
					return nil, err
				}

				r = bytes.NewReader(b)
			}

			docs, err := decodeDocuments(r)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f, err)
			}

			if len(docs) == 0 {
				return nil, fmt.Errorf("%s: no %s resources", f, apiVersion)
			}

			out = append(out, docs...)
		}
	}

	return out, nil
}

func documentFiles(name string) ([]string, error) {
	fi, statErr := os.Stat(name)
	if isDirectory := statErr == nil && fi.IsDir(); name == "-" || !isDirectory {
		return []string{
			name,
		}, nil
	}

	entries, err := os.ReadDir(name)
	if err != nil {
		return nil, err
	}

	var paths []string

	for _, e := range entries {
		if ext := filepath.Ext(e.Name()); !e.IsDir() && (ext == ".yaml" || ext == ".yml") {
			paths = append(paths, filepath.Join(name, e.Name()))
		}
	}

	if len(paths) == 0 {
		return nil, fmt.Errorf("%s: no .yaml or .yml files", name)
	}

	return paths, nil
}

func decodeDocuments(r io.Reader) ([]document, error) {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)

	var out []document

	for i := 1; ; i++ {
		var y resourceYAML

		err := dec.Decode(&y)
		if errors.Is(err, io.EOF) {
			return out, nil
		}

		if err != nil {
			return nil, fmt.Errorf("document %d: %w", i, err)
		}

		if y.APIVersion == "" && y.Kind == "" && y.Metadata.Name == "" && y.Spec == nil {
			continue
		}

		d, err := decode(y)
		if err != nil {
			return nil, fmt.Errorf("document %d: %w", i, err)
		}

		out = append(out, d)
	}
}

func decode(y resourceYAML) (document, error) {
	if y.APIVersion != apiVersion {
		return document{}, fmt.Errorf("apiVersion is %q, want %s", y.APIVersion, apiVersion)
	}

	k, err := kindNamed(y.Kind)
	if err != nil || k.name != y.Kind {
		return document{}, fmt.Errorf("unknown kind %q", y.Kind)
	}

	if y.Metadata.Name == "" {
		return document{}, errors.New("metadata.name is required")
	}

	spec := map[string]any(y.Spec)
	if spec == nil {
		spec = map[string]any{}
	}

	nameKey := jsonName(k.new(), k.nameField)
	if n, ok := spec[nameKey]; ok && n != y.Metadata.Name {
		return document{}, fmt.Errorf("spec.%s %v differs from metadata.name %q", nameKey, n, y.Metadata.Name)
	}

	spec[nameKey] = y.Metadata.Name
	if k.prepare != nil {
		k.prepare(spec)
	}

	expandEnums(spec, k.new().ProtoReflect().Descriptor())

	b, err := json.Marshal(spec)
	if err != nil {
		return document{}, fmt.Errorf("spec: %w", err)
	}

	m := k.new()
	if err := protojson.Unmarshal(b, m); err != nil {
		return document{}, fmt.Errorf("spec of %s %s: %w", y.Kind, y.Metadata.Name, err)
	}

	owned := map[string]bool{}

	fields := m.ProtoReflect().Descriptor().Fields()
	for key := range spec {
		f := fields.ByJSONName(key)
		if f == nil {
			f = fields.ByName(protoreflect.Name(key))
		}

		if f == nil {
			continue
		}

		owned[f.JSONName()] = true

		if o := f.ContainingOneof(); o != nil && !o.IsSynthetic() {
			for i := range o.Fields().Len() {
				owned[o.Fields().Get(i).JSONName()] = true
			}
		}
	}

	return document{
		kind:  k,
		name:  y.Metadata.Name,
		msg:   m,
		owned: owned,
	}, nil
}

func jsonName(m proto.Message, field string) string {
	return m.ProtoReflect().Descriptor().Fields().ByName(protoreflect.Name(field)).JSONName()
}

func nameOf(m proto.Message, field string) string {
	r := m.ProtoReflect()
	return r.Get(r.Descriptor().Fields().ByName(protoreflect.Name(field))).String()
}

func restrict(m proto.Message, owned map[string]bool, k *kind) proto.Message {
	c := proto.Clone(m)
	r := c.ProtoReflect()

	fields := r.Descriptor().Fields()
	for i := range fields.Len() {
		if f := fields.Get(i); !owned[f.JSONName()] {
			r.Clear(f)
		}
	}

	for _, p := range k.ignored {
		clearPath(r, strings.Split(p, "."))
	}

	for _, name := range k.sets {
		if f := fields.ByJSONName(name); f != nil && f.IsList() && r.Has(f) {
			r.Set(f, sortedSet(r, f))
		}
	}

	return c
}

func sortedSet(r protoreflect.Message, f protoreflect.FieldDescriptor) protoreflect.Value {
	list := r.Get(f).List()

	values := make([]string, 0, list.Len())
	for i := range list.Len() {
		values = append(values, list.Get(i).String())
	}

	slices.Sort(values)

	out := r.NewField(f).List()
	for _, v := range slices.Compact(values) {
		out.Append(protoreflect.ValueOfString(v))
	}

	return protoreflect.ValueOfList(out)
}

func clearPath(r protoreflect.Message, path []string) {
	f := r.Descriptor().Fields().ByJSONName(path[0])
	switch {
	case f == nil:
	case len(path) == 1:
		r.Clear(f)
	case f.Message() != nil && !f.IsList() && !f.IsMap() && r.Has(f):
		clearPath(r.Mutable(f).Message(), path[1:])
	}
}

func merged(live proto.Message, d document) proto.Message {
	out := proto.Clone(live)
	r, want := out.ProtoReflect(), d.msg.ProtoReflect()

	fields := r.Descriptor().Fields()
	for i := range fields.Len() {
		f := fields.Get(i)
		if !d.owned[f.JSONName()] {
			continue
		}

		r.Clear(f)

		if want.Has(f) {
			r.Set(f, want.Get(f))
		}
	}

	return out
}

func toYAML(k *kind, m proto.Message) ([]byte, error) {
	b, err := protojson.Marshal(m)
	if err != nil {
		return nil, err
	}

	spec := map[string]any{}
	if err := json.Unmarshal(b, &spec); err != nil {
		return nil, err
	}

	delete(spec, jsonName(m, k.nameField))

	var out bytes.Buffer

	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)

	if err := enc.Encode(
		resourceYAML{
			APIVersion: apiVersion,
			Kind:       k.name,
			Metadata: metadataYAML{
				Name: nameOf(m, k.nameField),
			},
			Spec: spec,
		},
	); err != nil {
		return nil, err
	}

	if err := enc.Close(); err != nil {
		return nil, err
	}

	return out.Bytes(), nil
}
