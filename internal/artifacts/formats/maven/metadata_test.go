package maven

import (
	"reflect"
	"strings"
	"testing"
)

func TestMergeSnapshotMetadata(t *testing.T) {
	t.Parallel()

	older, err := Parse(strings.NewReader(`<metadata><versioning>
  <snapshot><timestamp>20260101.000000</timestamp><buildNumber>1</buildNumber></snapshot>
  <snapshotVersions>
    <snapshotVersion><extension>jar</extension><value>1.0-20260101.000000-1</value><updated>20260101000000</updated></snapshotVersion>
    <snapshotVersion><classifier>sources</classifier><extension>jar</extension><value>1.0-20260101.000000-1</value><updated>20260101000000</updated></snapshotVersion>
  </snapshotVersions>
</versioning></metadata>`))
	if err != nil {
		t.Fatal(err)
	}

	newer, err := Parse(strings.NewReader(`<metadata><versioning>
  <snapshot><timestamp>20260102.000000</timestamp><buildNumber>2</buildNumber></snapshot>
  <snapshotVersions>
    <snapshotVersion><extension>jar</extension><value>1.0-20260102.000000-2</value><updated>20260102000000</updated></snapshotVersion>
    <snapshotVersion><extension>pom</extension><value>1.0-20260102.000000-2</value><updated>20260102000000</updated></snapshotVersion>
  </snapshotVersions>
</versioning></metadata>`))
	if err != nil {
		t.Fatal(err)
	}

	got := Merge([]Metadata{
		older,
		newer,
	})

	if got.Versioning == nil || got.Versioning.Snapshot == nil ||
		got.Versioning.Snapshot.Timestamp != "20260102.000000" {
		t.Fatalf("merged versioning = %+v, want the newer snapshot", got.Versioning)
	}

	want := []SnapshotVersion{
		{
			Extension: "jar",
			Value:     "1.0-20260102.000000-2",
			Updated:   "20260102000000",
		},
		{
			Classifier: "sources",
			Extension:  "jar",
			Value:      "1.0-20260101.000000-1",
			Updated:    "20260101000000",
		},
		{
			Extension: "pom",
			Value:     "1.0-20260102.000000-2",
			Updated:   "20260102000000",
		},
	}
	if !reflect.DeepEqual(got.Versioning.SnapshotVersions, want) {
		t.Errorf("snapshot versions = %+v, want %+v", got.Versioning.SnapshotVersions, want)
	}
}

func TestMergeGroupMetadata(t *testing.T) {
	t.Parallel()

	local, err := Parse(strings.NewReader(`<metadata><plugins>
  <plugin><name>Relic Plugin</name><prefix>relic</prefix><artifactId>relic-maven-plugin</artifactId></plugin>
</plugins></metadata>`))
	if err != nil {
		t.Fatal(err)
	}

	central, err := Parse(strings.NewReader(`<metadata><plugins>
  <plugin><name>Versions Maven Plugin</name><prefix>versions</prefix><artifactId>versions-maven-plugin</artifactId></plugin>
  <plugin><name>Shadowed</name><prefix>relic</prefix><artifactId>other-plugin</artifactId></plugin>
</plugins></metadata>`))
	if err != nil {
		t.Fatal(err)
	}

	got := Merge([]Metadata{
		local,
		central,
	})

	want := []Plugin{
		{
			Name:       "Relic Plugin",
			Prefix:     "relic",
			ArtifactID: "relic-maven-plugin",
		},
		{
			Name:       "Versions Maven Plugin",
			Prefix:     "versions",
			ArtifactID: "versions-maven-plugin",
		},
	}
	if !reflect.DeepEqual(got.Plugins, want) {
		t.Errorf("plugins = %+v, want %+v", got.Plugins, want)
	}

	if got.Versioning != nil {
		t.Errorf("group metadata got versioning %+v", got.Versioning)
	}

	out, err := Marshal(got)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(out), "<prefix>versions</prefix>") || strings.Contains(string(out), "<versioning>") {
		t.Errorf("merged document:\n%s", out)
	}
}
