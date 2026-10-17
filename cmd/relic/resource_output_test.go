package main

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"

	"google.golang.org/protobuf/proto"

	relicv1 "relic/api/gen/relic/v1"
)

type namedJSON struct {
	Name string `json:"name"`
}

func TestPrintResourcesAsJSON(t *testing.T) {
	t.Parallel()

	group, err := kindNamed("Group")
	if err != nil {
		t.Fatal(err)
	}

	devs := &relicv1.Group{
		Name: "devs",
	}
	ops := &relicv1.Group{
		Name: "ops",
	}

	tests := []struct {
		name      string
		items     []proto.Message
		list      bool
		wantNames []string
	}{
		{
			name:      "an empty list",
			list:      true,
			wantNames: []string{},
		},
		{
			name: "a list",
			items: []proto.Message{
				devs,
				ops,
			},
			list: true,
			wantNames: []string{
				"devs",
				"ops",
			},
		},
		{
			name: "one resource",
			items: []proto.Message{
				devs,
			},
			wantNames: []string{
				"devs",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer
			if err := printResources(&out, group, tt.items, "json", tt.list); err != nil {
				t.Fatal(err)
			}

			names := []string{}

			if tt.list {
				var got []namedJSON
				if err := json.Unmarshal(out.Bytes(), &got); err != nil {
					t.Fatalf("output is not one JSON array: %v\n%s", err, out.String())
				}

				for _, g := range got {
					names = append(names, g.Name)
				}
			} else {
				var got namedJSON
				if err := json.Unmarshal(out.Bytes(), &got); err != nil {
					t.Fatalf("output is not one JSON object: %v\n%s", err, out.String())
				}

				names = append(names, got.Name)
			}

			if !slices.Equal(names, tt.wantNames) {
				t.Errorf("names = %v, want %v", names, tt.wantNames)
			}
		})
	}
}
