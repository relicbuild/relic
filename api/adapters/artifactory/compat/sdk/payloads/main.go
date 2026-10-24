// Command payloads writes the JSON documents the adapter's round-trip tests
// read, built from the client libraries' types.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/jfrog/build-info-go/entities"
	"github.com/jfrog/jfrog-client-go/artifactory/services"
)

const (
	repositoriesDir = "api/adapters/artifactory/repoconfig/testdata/sdk"
	permissionsDir  = "api/adapters/artifactory/access/testdata/sdk"
	buildsDir       = "api/adapters/artifactory/buildjson/testdata/sdk"
)

func main() {
	root := flag.String("root", "../../../../..", "the Relic repository the documents are written into")

	flag.Parse()

	documents, err := payloadDocuments()
	if err != nil {
		fmt.Fprintln(os.Stderr, "payloads:", err)
		os.Exit(1)
	}

	if err := writeDocuments(*root, documents); err != nil {
		fmt.Fprintln(os.Stderr, "payloads:", err)
		os.Exit(1)
	}
}

func writeDocuments(root string, documents map[string][]byte) error {
	for _, dir := range []string{
		repositoriesDir,
		permissionsDir,
		buildsDir,
	} {
		if err := os.RemoveAll(filepath.Join(root, dir)); err != nil {
			return err
		}

		if err := os.MkdirAll(filepath.Join(root, dir), 0o750); err != nil {
			return err
		}
	}

	for path, content := range documents {
		if err := os.WriteFile(filepath.Join(root, path), content, 0o600); err != nil {
			return err
		}
	}

	return nil
}

func payloadDocuments() (map[string][]byte, error) {
	documents := map[string][]byte{}

	add := func(path string, v any) error {
		var buf bytes.Buffer

		encoder := json.NewEncoder(&buf)
		encoder.SetEscapeHTML(false)
		encoder.SetIndent("", "  ")

		if err := encoder.Encode(v); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}

		documents[path] = buf.Bytes()

		return nil
	}

	for _, params := range repositoryParams() {
		filled := reflect.New(reflect.TypeOf(params)).Elem()
		filled.Set(reflect.ValueOf(params))

		s := sample{
			rclass:      filled.FieldByName("Rclass").String(),
			packageType: filled.FieldByName("PackageType").String(),
		}
		if s.rclass == "" || s.packageType == "" {
			return nil, fmt.Errorf("%T has no rclass or packageType", params)
		}

		s.fill(filled)

		if err := add(
			filepath.Join(repositoriesDir, s.rclass+"-"+s.packageType+".json"),
			filled.Interface(),
		); err != nil {
			return nil, err
		}
	}

	for name, permission := range permissionTargets() {
		if err := add(filepath.Join(permissionsDir, name+".json"), permission); err != nil {
			return nil, err
		}
	}

	for name, document := range buildDocuments() {
		if err := add(filepath.Join(buildsDir, name+".json"), document); err != nil {
			return nil, err
		}
	}

	return documents, nil
}

type sample struct {
	rclass      string
	packageType string
}

var omittedFields = map[string]bool{
	"password": true,
}

func (s sample) fill(v reflect.Value) {
	t := v.Type()

	for i := range t.NumField() {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}

		if field.Anonymous {
			s.fill(v.Field(i))
			continue
		}

		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "" || name == "-" || omittedFields[name] || !v.Field(i).IsZero() {
			continue
		}

		s.set(v.Field(i), name)
	}
}

func (s sample) set(v reflect.Value, name string) {
	switch v.Kind() {
	case reflect.Pointer:
		elem := reflect.New(v.Type().Elem())
		s.set(elem.Elem(), name)
		v.Set(elem)
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int32, reflect.Int64:
		v.SetInt(int64(sampleNumber(name)))
	case reflect.String:
		v.SetString(s.text(name))
	case reflect.Slice:
		elem := reflect.New(v.Type().Elem()).Elem()
		if elem.Kind() == reflect.String {
			elem.SetString(s.listEntry(name))
		} else {
			s.set(elem, name)
		}

		v.Set(reflect.Append(reflect.MakeSlice(v.Type(), 0, 1), elem))
	case reflect.Struct:
		s.fill(v)
	default:
		panic(fmt.Sprintf("payloads: no sample for %s of kind %s", name, v.Kind()))
	}
}

func (s sample) key() string {
	return "example-" + s.packageType + "-" + s.rclass
}

func (s sample) text(name string) string {
	switch name {
	case "key":
		return s.key()
	case "url":
		return "https://example.com/" + s.packageType
	case "defaultDeploymentRepo":
		return "example-" + s.packageType + "-local"
	case "checksumPolicyType":
		return "client-checksums"
	case "snapshotVersionBehavior":
		return "unique"
	case "dockerApiVersion":
		return "V2"
	case "pomRepositoryReferencesCleanupPolicy":
		return "discard_active_reference"
	case "remoteRepoChecksumPolicyType":
		return "generate-if-absent"
	case "vcsGitProvider":
		return "GITHUB"
	case "vcsType":
		return "GIT"
	case "repoLayoutRef":
		return "simple-default"
	case "debianDefaultArchitectures":
		return "amd64,arm64"
	case "mismatchingMimeTypesOverrideList":
		return "application/json"
	case "yumGroupFileNames":
		return "groups.xml"
	case "pyPIRepositorySuffix":
		return "simple"
	case "includesPattern":
		return "**/*"
	case "excludesPattern":
		return "tmp/**"
	case "localAddress":
		return "127.0.0.1"
	case "queryParams":
		return "region=example"
	}

	if strings.HasSuffix(name, "Url") {
		return "https://example.com/" + strings.TrimSuffix(name, "Url")
	}

	return "example-" + name
}

func (s sample) listEntry(name string) string {
	switch name {
	case "environments":
		return "DEV"
	case "repositories":
		return "example-" + s.packageType + "-local"
	case "propertySets":
		return "artifactory"
	case "optionalIndexCompressionFormats":
		return "bz2"
	case "externalDependenciesPatterns":
		return "**/github.com/**"
	}

	return "example-" + name
}

func sampleNumber(name string) int {
	switch name {
	case "retrievalCachePeriodSecs":
		return 7200
	case "missedRetrievalCachePeriodSecs":
		return 1800
	case "metadataRetrievalTimeoutSecs":
		return 60
	case "socketTimeoutMillis":
		return 15000
	case "assumedOfflinePeriodSecs":
		return 300
	case "unusedArtifactsCleanupPeriodHours":
		return 24
	case "virtualRetrievalCachePeriodSecs":
		return 600
	}

	return 3
}

func permissionTargets() map[string]services.PermissionTargetParams {
	return map[string]services.PermissionTargetParams{
		"repositories": {
			Name: "example-repositories",
			Repo: &services.PermissionTargetSection{
				IncludePatterns: []string{
					"**",
				},
				ExcludePatterns: []string{
					"secret/**",
				},
				Repositories: []string{
					"example-generic-local",
					"example-maven-remote",
				},
				Actions: &services.Actions{
					Users: map[string][]string{
						"example-user": {
							"read",
							"write",
							"annotate",
						},
					},
					Groups: map[string][]string{
						"example-group": {
							"read",
							"write",
							"annotate",
							"delete",
							"manage",
						},
					},
				},
			},
		},
		"any-repository": {
			Name: "example-any-repository",
			Repo: &services.PermissionTargetSection{
				IncludePatterns: []string{
					"**",
				},
				ExcludePatterns: []string{
					"**/*.tmp",
				},
				Repositories: []string{
					"ANY LOCAL",
					"ANY REMOTE",
				},
				Actions: &services.Actions{
					Groups: map[string][]string{
						"readers": {
							"read",
						},
					},
				},
			},
		},
		"builds": {
			Name: "example-builds",
			Build: &services.PermissionTargetSection{
				IncludePatterns: []string{
					"example-build/**",
				},
				ExcludePatterns: []string{
					"example-build/scratch/**",
				},
				Repositories: []string{
					"artifactory-build-info",
				},
				Actions: &services.Actions{
					Users: map[string][]string{
						"example-ci": {
							"read",
							"write",
						},
					},
				},
			},
		},
	}
}

func buildDocuments() map[string]any {
	falseValue, trueValue := false, true

	return map[string]any{
		"build": entities.BuildInfo{
			Name:    "example-build",
			Number:  "1",
			Started: "2026-09-23T10:00:00.000+0000",
			Agent: &entities.Agent{
				Name:    "jfrog-cli-go",
				Version: "2.124.0",
			},
			BuildAgent: &entities.Agent{
				Name:    "GENERIC",
				Version: "2.124.0",
			},
			Properties: map[string]string{
				"buildInfo.env.CI": "true",
			},
			VcsList: []entities.Vcs{
				{
					Url:      "https://example.com/example.git",
					Revision: "0123456789abcdef0123456789abcdef01234567",
					Branch:   "main",
					Message:  "example change",
				},
			},
			Modules: []entities.Module{
				{
					Id:   "example-module",
					Type: entities.Generic,
					Artifacts: []entities.Artifact{
						{
							Name: "app.bin",
							Type: "bin",
							Path: "dist/app.bin",
							Checksum: entities.Checksum{
								Sha1:   "0123456789abcdef0123456789abcdef01234567",
								Md5:    "0123456789abcdef0123456789abcdef",
								Sha256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
							},
						},
					},
					Dependencies: []entities.Dependency{
						{
							Id:   "lib.bin",
							Type: "bin",
							Checksum: entities.Checksum{
								Sha1: "89abcdef0123456789abcdef0123456789abcdef",
								Md5:  "89abcdef0123456789abcdef01234567",
							},
						},
					},
				},
			},
		},
		"promotion": services.BuildPromotionBody{
			Comment:             "example promotion",
			SourceRepo:          "example-generic-local",
			TargetRepo:          "example-generic-target",
			Status:              "released",
			IncludeDependencies: &falseValue,
			Copy:                &trueValue,
			FailFast:            true,
			DryRun:              &falseValue,
			Properties: map[string][]string{
				"release": {
					"1.0",
				},
			},
		},
		"retention": services.DiscardBuildsBody{
			MinimumBuildDate: "2026-01-01T00:00:00.000+0000",
			MaxBuilds:        "5",
			ExcludeBuilds: []string{
				"1",
			},
			DeleteArtifacts: true,
		},
		"retention-count-zero": services.DiscardBuildsBody{
			MaxBuilds: "0",
		},
	}
}

func readDocuments(root string) (map[string][]byte, error) {
	documents := map[string][]byte{}

	for _, dir := range []string{
		repositoriesDir,
		permissionsDir,
		buildsDir,
	} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}

			content, err := os.ReadFile(path) //nolint:gosec // G304: the committed documents
			if err != nil {
				return err
			}

			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}

			documents[rel] = content

			return nil
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}

	return documents, nil
}
