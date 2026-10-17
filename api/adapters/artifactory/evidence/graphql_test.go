package evidence

import "testing"

func TestParseQuery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		query     string
		kind      string
		args      map[string]string
		predicate bool
		wantErr   bool
	}{
		{
			name:  "artifact",
			query: `{ evidence { searchEvidence( where: { hasSubjectWith: { repositoryKey: "r", path: "a/b", name: "f.txt"}} ) { totalCount edges { node { predicateSlug predicateType downloadPath verified signingKey { alias } createdBy createdAt subject { sha256 } attachments { name sha256 type downloadPath } } } } } }`,
			kind:  "searchEvidence",
			args: map[string]string{
				"repositoryKey": "r",
				"path":          "a/b",
				"name":          "f.txt",
			},
		},
		{
			name:  "artifact at root with predicate",
			query: `{ evidence { searchEvidence( where: { hasSubjectWith: { repositoryKey: "r", path: "", name: "f"}} ) { edges { node { predicateType predicate } } } } }`,
			kind:  "searchEvidence",
			args: map[string]string{
				"repositoryKey": "r",
				"path":          "",
				"name":          "f",
			},
			predicate: true,
		},
		{
			name:  "release bundle",
			query: `{ releaseBundleVersion { getVersion(repositoryKey: "release-bundles-v2", name: "app", version: "1.0") { evidenceConnection { edges { node { predicateSlug } } } artifactsConnection(first: 1000, after: "YXJ0aWZhY3Q6MA==", where: { hasEvidence: true }) { totalCount } } } }`,
			kind:  "releaseBundleVersion",
			args: map[string]string{
				"repositoryKey": "release-bundles-v2",
				"name":          "app",
				"version":       "1.0",
			},
		},
		{
			name:  "artifact whose path holds a brace",
			query: `{ evidence { searchEvidence( where: { hasSubjectWith: { repositoryKey: "r", path: "a}b", name: "f.txt"}} ) { totalCount } } }`,
			kind:  "searchEvidence",
			args: map[string]string{
				"repositoryKey": "r",
				"path":          "a}b",
				"name":          "f.txt",
			},
		},
		{
			name:  "release bundle whose name holds a parenthesis",
			query: `{ releaseBundleVersion { getVersion(repositoryKey: "release-bundles-v2", name: "app)", version: "1.0") { evidenceConnection { edges { node { predicateSlug } } } } } }`,
			kind:  "releaseBundleVersion",
			args: map[string]string{
				"repositoryKey": "release-bundles-v2",
				"name":          "app)",
				"version":       "1.0",
			},
		},
		{
			name:    "entity",
			query:   `{ evidence { searchEvidence(where: { hasSubjectWith: { entityType: "x", entityId: "y"}}) { edges { node { verified } } } } }`,
			wantErr: true,
		},
		{
			name:    "release bundle without a closing parenthesis",
			query:   `{ releaseBundleVersion { getVersion(repositoryKey: "release-bundles-v2", name: "app" { evidenceConnection } } }`,
			wantErr: true,
		},
		{
			name:    "other",
			query:   `{ something }`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			q, err := parseQuery(tt.query)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}

			if tt.wantErr {
				return
			}

			if q.kind != tt.kind || q.includePredicate != tt.predicate {
				t.Errorf("kind = %s, predicate = %v", q.kind, q.includePredicate)
			}

			for k, v := range tt.args {
				if got, ok := q.args[k]; !ok || got != v {
					t.Errorf("arg %s = %q, want %q", k, got, v)
				}
			}
		})
	}
}
