package main

import (
	"reflect"
	"testing"

	relicv1 "relic/api/gen/relic/v1"
)

func TestExpandEnums(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		spec map[string]any
		want map[string]any
	}{
		{
			name: "shorthands",
			spec: map[string]any{
				"class":  "local",
				"format": "helm-oci",
			},
			want: map[string]any{
				"class":  "REPOSITORY_CLASS_LOCAL",
				"format": "FORMAT_HELM_OCI",
			},
		},
		{
			name: "full names and unknown values stay",
			spec: map[string]any{
				"class":  "REPOSITORY_CLASS_REMOTE",
				"format": "gems",
			},
			want: map[string]any{
				"class":  "REPOSITORY_CLASS_REMOTE",
				"format": "gems",
			},
		},
		{
			name: "case and separators do not matter",
			spec: map[string]any{
				"format": "RubyGems",
			},
			want: map[string]any{
				"format": "FORMAT_RUBYGEMS",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			expandEnums(tt.spec, (&relicv1.Repository{}).ProtoReflect().Descriptor())

			if !reflect.DeepEqual(tt.spec, tt.want) {
				t.Errorf("got %v, want %v", tt.spec, tt.want)
			}
		})
	}
}
