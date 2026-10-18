package buildjson

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"relic/internal/delivery/buildinfo"
)

func sdkDocument(t *testing.T, name string) json.RawMessage {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("testdata", "sdk", name+".json"))
	if err != nil {
		t.Fatal(err)
	}

	return raw
}

func TestParseSDKBuild(t *testing.T) {
	t.Parallel()

	doc, err := Format{}.Parse(sdkDocument(t, "build"))
	if err != nil {
		t.Fatal(err)
	}

	if doc.Name != "example-build" || doc.Number != "1" || doc.Started.IsZero() {
		t.Errorf("identity = %q %q %v", doc.Name, doc.Number, doc.Started)
	}

	if doc.Agent.Name != "jfrog-cli-go" || len(doc.VCS) != 1 || len(doc.Modules) != 1 ||
		len(doc.Modules[0].Artifacts) != 1 || len(doc.Modules[0].Dependencies) != 1 {
		t.Fatalf("document = %+v", doc)
	}

	if a := doc.Modules[0].Artifacts[0]; a.Name != "app.bin" || a.Path != "dist/app.bin" || a.SHA1 == "" ||
		a.SHA256 == "" {
		t.Errorf("artifact = %+v", a)
	}

	if d := doc.Modules[0].Dependencies[0]; d.ID != "lib.bin" || d.SHA1 == "" || d.SHA256 != "" {
		t.Errorf("dependency = %+v", d)
	}
}

func TestParse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		body    string
		want    buildinfo.Document
		wantErr bool
	}{
		{
			name: "every field Relic reads",
			body: `{"name":"b","number":"7","started":"2026-01-02T03:04:05.006+0100","agent":{"name":"ci","version":"1"},
				"vcs":[{"url":"https://git.example/r","revision":"abc","branch":"main","message":"m"}],
				"properties":{"k":"v"},
				"modules":[{"id":"m","type":"npm","artifacts":[{"name":"a.tgz","type":"tgz","path":"p/a.tgz","sha1":"1","sha256":"2","md5":"3"}],
				"dependencies":[{"id":"d:1","type":"tgz","scopes":["prod"],"sha1":"4","sha256":"5","md5":"6"}]}]}`,
			want: buildinfo.Document{
				Name:    "b",
				Number:  "7",
				Started: time.Date(2026, 1, 2, 2, 4, 5, 6e6, time.UTC),
				Agent: buildinfo.Agent{
					Name:    "ci",
					Version: "1",
				},
				VCS: []buildinfo.VCS{
					{
						URL:      "https://git.example/r",
						Revision: "abc",
						Branch:   "main",
					},
				},
				Modules: []buildinfo.Module{
					{
						ID:   "m",
						Type: "npm",
						Artifacts: []buildinfo.Artifact{
							{
								Name:   "a.tgz",
								Type:   "tgz",
								Path:   "p/a.tgz",
								SHA1:   "1",
								SHA256: "2",
								MD5:    "3",
							},
						},
						Dependencies: []buildinfo.Dependency{
							{
								ID:   "d:1",
								Type: "tgz",
								Scopes: []string{
									"prod",
								},
								SHA1:   "4",
								SHA256: "5",
								MD5:    "6",
							},
						},
					},
				},
				Properties: map[string]string{
					"k": "v",
				},
			},
		},
		{
			name: "RFC 3339 start",
			body: `{"name":"b","number":"1","started":"2026-01-02T03:04:05Z"}`,
			want: buildinfo.Document{
				Name:    "b",
				Number:  "1",
				Started: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
			},
		},
		{
			name:    "bad start",
			body:    `{"name":"b","number":"1","started":"yesterday"}`,
			wantErr: true,
		},
		{
			name:    "not JSON",
			body:    `{`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := Format{}.Parse([]byte(tt.body))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}

			if tt.wantErr {
				return
			}

			if !got.Started.Equal(tt.want.Started) {
				t.Errorf("started = %v, want %v", got.Started, tt.want.Started)
			}

			got.Started = tt.want.Started
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got  %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestPromotionRoundTripSDKBody(t *testing.T) {
	t.Parallel()

	raw := sdkDocument(t, "promotion")

	var body Promotion
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}

	p := body.Native()
	if p.TargetRepository != "example-generic-target" || p.Status != "released" || !p.Copy || !p.FailFast {
		t.Errorf("native = %+v", p)
	}

	out, err := json.Marshal(PromotionOf(p))
	if err != nil {
		t.Fatal(err)
	}

	var got, want any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}

	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %s\nwant %s", out, raw)
	}
}

func TestRetentionRoundTripSDKBody(t *testing.T) {
	t.Parallel()

	raw := sdkDocument(t, "retention")

	var body Retention
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}

	r, err := body.Native()
	if err != nil {
		t.Fatal(err)
	}

	out, err := json.Marshal(RetentionOf(r))
	if err != nil {
		t.Fatal(err)
	}

	if string(out) != string(compact(t, raw)) {
		t.Errorf("got %s, want %s", out, raw)
	}

	var zero Retention
	if err := json.Unmarshal(sdkDocument(t, "retention-count-zero"), &zero); err != nil {
		t.Fatal(err)
	}

	if _, err := zero.Native(); !errors.Is(err, buildinfo.ErrInvalid) {
		t.Errorf("count 0: err = %v, want ErrInvalid", err)
	}
}

func TestRetention(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		body    Retention
		want    buildinfo.Retention
		wantErr bool
	}{
		{
			name: "no limits",
			body: Retention{
				DeleteArtifacts: true,
			},
			want: buildinfo.Retention{
				DeleteArtifacts: true,
			},
		},
		{
			name: "count and date",
			body: Retention{
				Count:            "3",
				MinimumBuildDate: "2026-01-02T03:04:05.000+0000",
				ExcludeBuilds: []string{
					"1",
				},
			},
			want: buildinfo.Retention{
				MaxRuns:    3,
				MinStarted: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
				Keep: []string{
					"1",
				},
			},
		},
		{
			name: "zero count",
			body: Retention{
				Count: "0",
			},
			wantErr: true,
		},
		{
			name: "bad count",
			body: Retention{
				Count: "many",
			},
			wantErr: true,
		},
		{
			name: "bad date",
			body: Retention{
				MinimumBuildDate: "2026-01-02",
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := tt.body.Native()
			if tt.wantErr {
				if !errors.Is(err, buildinfo.ErrInvalid) {
					t.Fatalf("err = %v, want ErrInvalid", err)
				}

				return
			}

			if err != nil {
				t.Fatal(err)
			}

			if !got.MinStarted.Equal(tt.want.MinStarted) {
				t.Errorf("min started = %v", got.MinStarted)
			}

			got.MinStarted = tt.want.MinStarted
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}

			if back := RetentionOf(got); !reflect.DeepEqual(back, tt.body) {
				t.Errorf("back = %+v, want %+v", back, tt.body)
			}
		})
	}
}

func TestMessages(t *testing.T) {
	t.Parallel()

	got := Messages(
		[]buildinfo.Message{
			{
				Level: buildinfo.LevelInfo,
				Text:  "copied",
			},
			{
				Level: buildinfo.LevelWarning,
				Text:  "skipped",
			},
			{
				Level: buildinfo.LevelError,
				Text:  "failed",
			},
		},
	)

	want := []Message{
		{
			Level:   "WARNING",
			Message: "skipped",
		},
		{
			Level:   "ERROR",
			Message: "failed",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}

	if got := Messages(nil); got == nil {
		t.Error("an empty report is nil; the client reads an array")
	}
}

func TestView(t *testing.T) {
	t.Parallel()

	promoted := time.Date(2026, 9, 24, 8, 30, 0, 0, time.UTC)
	want := []status{
		{
			Status:        "released",
			Comment:       "example promotion",
			Repository:    "example-generic-target",
			Timestamp:     "2026-09-24T08:30:00.000+0000",
			User:          "admin",
			TimestampDate: promoted.UnixMilli(),
		},
		{
			Status:        "rolled-back",
			Repository:    "example-generic-local",
			Timestamp:     "2026-09-24T09:30:00.000+0000",
			User:          "admin",
			TimestampDate: promoted.Add(time.Hour).UnixMilli(),
		},
	}

	run := buildinfo.BuildRun{
		Raw: sdkDocument(t, "build"),
	}
	for _, s := range want {
		run.Statuses = append(run.Statuses, buildinfo.Status{
			Status:     s.Status,
			Comment:    s.Comment,
			Repository: s.Repository,
			User:       s.User,
			Created:    time.UnixMilli(s.TimestampDate).UTC(),
		})
	}

	view, err := View(run)
	if err != nil {
		t.Fatal(err)
	}

	var got struct {
		Statuses []status `json:"statuses"`
		Name     string   `json:"name"`
	}
	if err := json.Unmarshal(view, &got); err != nil {
		t.Fatal(err)
	}

	if got.Name != "example-build" || len(got.Statuses) != len(want) {
		t.Fatalf("view = %s", view)
	}

	for i := range want {
		if got.Statuses[i] != want[i] {
			t.Errorf("status %d = %+v, want %+v", i, got.Statuses[i], want[i])
		}
	}

	if view, err := View(buildinfo.BuildRun{
		Raw: json.RawMessage(`{"name":"b"}`),
	}); err != nil ||
		string(view) != `{"name":"b"}` {
		t.Errorf("a run without promotions = %s %v", view, err)
	}
}

func compact(t *testing.T, b []byte) []byte {
	t.Helper()

	var v Retention
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}

	out, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}

	return out
}

func TestRenderRoundTrip(t *testing.T) {
	t.Parallel()

	doc := buildinfo.Document{
		Name:    "app",
		Number:  "7",
		Started: time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC),
		Agent: buildinfo.Agent{
			Name:    "ci",
			Version: "1.0",
		},
		VCS: []buildinfo.VCS{
			{
				URL:      "https://git.example/app",
				Revision: "abc",
				Branch:   "main",
			},
		},
		Modules: []buildinfo.Module{
			{
				ID:   "app:1",
				Type: "generic",
				Artifacts: []buildinfo.Artifact{
					{
						Name:   "app.bin",
						Type:   "bin",
						Path:   "dist/app.bin",
						SHA1:   "s1",
						SHA256: "s256",
						MD5:    "m5",
					},
				},
				Dependencies: []buildinfo.Dependency{
					{
						ID:   "lib:1",
						Type: "jar",
						Scopes: []string{
							"compile",
						},
						SHA1:   "d1",
						SHA256: "d256",
						MD5:    "dm5",
					},
				},
			},
		},
		Properties: map[string]string{
			"buildInfo.env.CI": "true",
		},
	}

	raw, err := Format{}.Render(doc)
	if err != nil {
		t.Fatal(err)
	}

	back, err := Format{}.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}

	back.Started = back.Started.UTC()
	if !reflect.DeepEqual(back, doc) {
		t.Errorf("round trip\n got %+v\nwant %+v\njson %s", back, doc, raw)
	}
}
