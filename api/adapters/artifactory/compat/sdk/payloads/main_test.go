package main

import (
	"slices"
	"testing"
)

func TestPayloadsUpToDate(t *testing.T) {
	t.Parallel()

	want, err := payloadDocuments()
	if err != nil {
		t.Fatal(err)
	}

	got, err := readDocuments("../../../../../..")
	if err != nil {
		t.Fatal(err)
	}

	for path, content := range want {
		if string(got[path]) != string(content) {
			t.Errorf("%s is stale; run go run ./payloads in api/adapters/artifactory/compat/sdk", path)
		}
	}

	for path := range got {
		if _, ok := want[path]; !ok {
			t.Errorf("%s is not generated; run go run ./payloads in api/adapters/artifactory/compat/sdk", path)
		}
	}

	if len(want) < 100 || !slices.ContainsFunc(sortedKeys(want), func(path string) bool {
		return path == repositoriesDir+"/local-maven.json"
	}) {
		t.Errorf("%d documents, want every repository class and package type", len(want))
	}
}

func sortedKeys(documents map[string][]byte) []string {
	keys := make([]string, 0, len(documents))
	for k := range documents {
		keys = append(keys, k)
	}

	slices.Sort(keys)

	return keys
}
