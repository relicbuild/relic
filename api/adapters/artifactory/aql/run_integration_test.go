//go:build integration

package aql_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"relic/api/adapters/artifactory/aql"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
)

func TestMain(m *testing.M) { databasetest.Main(m) }

type result struct {
	Results []map[string]any `json:"results"`
	Range   map[string]int64 `json:"range"`
}

func TestWriteItems(t *testing.T) {
	t.Parallel()
	db := databasetest.NewDB(t)
	svc := repository.NewService(db, objectstoretest.NewMemory(), slog.New(slog.DiscardHandler))

	ctx := t.Context()
	for _, key := range []string{
		"libs",
		"other",
	} {
		if err := svc.CreateRepository(
			ctx,
			repository.Repository{
				Name:  key,
				Class: repository.ClassLocal,
			},
		); err != nil {
			t.Fatal(err)
		}
	}

	for _, f := range []struct {
		repo, path, content string
		props               repository.Properties
	}{
		{
			"libs",
			"a/one.jar",
			"1",
			repository.Properties{
				"stage": {
					"dev",
				},
			},
		},
		{
			"libs",
			"a/b/two.jar",
			"22",
			repository.Properties{
				"stage": {
					"prod",
				},
				"team": {
					"x",
				},
			},
		},
		{
			"libs",
			"three.txt",
			"333",
			nil,
		},
		{
			"other",
			"a/four.jar",
			"4444",
			nil,
		},
	} {
		if _, err := svc.Deploy(
			ctx,
			repository.DeployInput{
				Repository: f.repo,
				Path:       f.path,
				Body:       strings.NewReader(f.content),
				Props:      f.props,
				User:       "admin",
			},
		); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name      string
		src       string
		wantNames []string
		check     func(t *testing.T, r result)
	}{
		{
			name: "repo files",
			src:  `items.find({"repo":"libs"}).sort({"$asc":["name"]})`,
			wantNames: []string{
				"one.jar",
				"three.txt",
				"two.jar",
			},
		},
		{
			name: "wildcard path",
			src:  `items.find({"repo":"libs","path":{"$match":"a*"},"name":{"$match":"*.jar"}}).sort({"$asc":["name"]})`,
			wantNames: []string{
				"one.jar",
				"two.jar",
			},
		},
		{
			name: "root path",
			src:  `items.find({"repo":"libs","path":"."})`,
			wantNames: []string{
				"three.txt",
			},
		},
		{
			name: "folders",
			src:  `items.find({"repo":"libs","type":"folder"}).sort({"$asc":["name"]})`,
			wantNames: []string{
				"a",
				"b",
			},
		},
		{
			name: "property",
			src:  `items.find({"@stage":"prod"})`,
			wantNames: []string{
				"two.jar",
			},
		},
		{
			name: "property wildcard",
			src:  `items.find({"@stage":{"$match":"*"}}).sort({"$asc":["name"]})`,
			wantNames: []string{
				"one.jar",
				"two.jar",
			},
		},
		{
			name: "not property",
			src:  `items.find({"repo":"libs","@stage":{"$ne":"dev"}}).sort({"$asc":["name"]})`,
			wantNames: []string{
				"three.txt",
				"two.jar",
			},
		},
		{
			name: "not property in $msp",
			src:  `items.find({"repo":"libs","$msp":[{"@stage":{"$ne":"dev"}}]})`,
			wantNames: []string{
				"two.jar",
			},
		},
		{
			name: "or across repos",
			src:  `items.find({"$or":[{"repo":"other"},{"name":"three.txt"}]}).sort({"$desc":["size"]})`,
			wantNames: []string{
				"four.jar",
				"three.txt",
			},
		},
		{
			name: "jf exclusions",
			src:  `items.find({"$or":[{"path":{"$nmatch":"*"},"name":{"$nmatch":"*.txt"}}],"$or":[{"repo":{"$nmatch":"*.txt"},"path":{"$nmatch":"*"},"name":{"$nmatch":"*"}}],"$or":[{"$and":[{"repo":"libs","path":{"$match":"*"},"name":{"$match":"*"}}]}]}).sort({"$asc":["name"]})`,
			wantNames: []string{
				"one.jar",
				"two.jar",
			},
		},
		{
			name: "jf explicit path",
			src:  `items.find({"path":{"$ne":"."},"$or":[{"$and":[{"repo":"libs","path":"a","name":"one.jar"}]}]}).include("name","repo","path","actual_md5","actual_sha1","sha256","size","type","modified","created","property")`,
			wantNames: []string{
				"one.jar",
			},
		},
		{
			name: "nulls sort last ascending",
			src:  `items.find({"repo":"other","type":"any"}).sort({"$asc":["actual_sha1"]})`,
			wantNames: []string{
				"four.jar",
				"a",
			},
		},
		{
			name: "nulls sort first descending",
			src:  `items.find({"repo":"other","type":"any"}).sort({"$desc":["actual_sha1"]})`,
			wantNames: []string{
				"a",
				"four.jar",
			},
		},
		{
			name: "size range",
			src:  `items.find({"size":{"$gte":2,"$lt":4}}).sort({"$asc":["size"]})`,
			wantNames: []string{
				"two.jar",
				"three.txt",
			},
		},
		{
			name: "include, offset and limit",
			src:  `items.find({"repo":"libs"}).include("name","actual_sha1","sha256","property").sort({"$asc":["name"]}).offset(1).limit(1)`,
			check: func(t *testing.T, r result) {
				t.Helper()

				if len(r.Results) != 1 || r.Results[0]["name"] != "three.txt" {
					t.Fatalf("results = %v", r.Results)
				}

				row := r.Results[0]
				if row["actual_sha1"] != "43814346e21444aaf4f70841bf7ed5ae93f55a9d" || row["repo"] != nil ||
					row["properties"] != nil {
					t.Errorf("row = %v", row)
				}

				if r.Range["start_pos"] != 1 || r.Range["end_pos"] != 2 || r.Range["total"] != 1 ||
					r.Range["limit"] != 1 {
					t.Errorf("range = %v", r.Range)
				}
			},
		},
		{
			name: "properties output",
			src:  `items.find({"name":"two.jar"}).include("name","property")`,
			check: func(t *testing.T, r result) {
				t.Helper()

				props, _ := json.Marshal(r.Results[0]["properties"])
				if string(props) != `[{"key":"stage","value":"prod"},{"key":"team","value":"x"}]` {
					t.Errorf("properties = %s", props)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			q, err := aql.Parse(tt.src)
			if err != nil {
				t.Fatal(err)
			}

			plan, err := aql.PlanItems(q)
			if err != nil {
				t.Fatal(err)
			}

			var buf bytes.Buffer
			if err := aql.WriteItems(t.Context(), db, plan, &buf, nil); err != nil {
				t.Fatal(err)
			}

			var r result
			if err := json.Unmarshal(buf.Bytes(), &r); err != nil {
				t.Fatalf("invalid JSON %s: %v", buf.String(), err)
			}

			if tt.check != nil {
				tt.check(t, r)
				return
			}

			var names []string
			for _, row := range r.Results {
				names = append(names, row["name"].(string))
			}

			if strings.Join(names, ",") != strings.Join(tt.wantNames, ",") {
				t.Errorf("names = %v, want %v", names, tt.wantNames)
			}

			if r.Range["total"] != int64(len(names)) {
				t.Errorf("range.total = %d, want %d", r.Range["total"], len(names))
			}
		})
	}
}
