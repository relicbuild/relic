//go:build integration

package integration

import (
	"encoding/json"
	"net/url"
	"slices"
	"testing"
	"time"
)

func TestNpmSearchAnswersWhatNpmReads(t *testing.T) {
	t.Parallel()

	srv := server(t)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/npm-local",
			body:   `{"rclass":"local","packageType":"npm"}`,
		},
	)

	base := "/artifactory/api/npm/npm-local"

	publish := func(name string, extra map[string]any) {
		t.Helper()

		var doc map[string]any
		if err := json.Unmarshal([]byte(publishDoc(name, "1.0.0", npmTarball(t, name, "1.0.0"))), &doc); err != nil {
			t.Fatal(err)
		}

		manifest := doc["versions"].(map[string]any)["1.0.0"].(map[string]any)
		for k, v := range extra {
			manifest[k] = v
		}

		body, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}

		if resp, reply := do(t, srv, call{
			method: "PUT",
			path:   base + "/" + name,
			body:   string(body),
		}); resp.StatusCode != 201 {
			t.Fatalf("publish %s = %d %s", name, resp.StatusCode, reply)
		}
	}

	publish("left-pad", map[string]any{
		"keywords": []string{
			"pad",
			"string",
		},
		"maintainers": []map[string]string{
			{
				"name":  "alice",
				"email": "alice@example.com",
			},
		},
	})
	publish("right-pad", nil)

	type person struct {
		Username string `json:"username"`
	}

	type result struct {
		Objects []struct {
			Package struct {
				Name        string    `json:"name"`
				Keywords    []string  `json:"keywords"`
				Date        string    `json:"date"`
				Publisher   *person   `json:"publisher"`
				Maintainers *[]person `json:"maintainers"`
			} `json:"package"`
		} `json:"objects"`
	}

	search := func(text string) result {
		t.Helper()

		var got result
		if code := getJSON(t, srv, base+"/-/v1/search?text="+url.QueryEscape(text), &got); code != 200 {
			t.Fatalf("search %q = %d", text, code)
		}

		return got
	}

	got := search("pad")
	if len(got.Objects) != 2 {
		t.Fatalf("search pad = %+v, want left-pad and right-pad", got)
	}

	wantMaintainers := map[string][]string{
		"left-pad": {
			"alice",
		},
		"right-pad": {
			"admin",
		},
	}

	for _, o := range got.Objects {
		p := o.Package
		if p.Maintainers == nil {
			t.Fatalf("%s has no maintainers, which npm search maps over", p.Name)
		}

		maintainers := make([]string, 0, len(*p.Maintainers))
		for _, m := range *p.Maintainers {
			maintainers = append(maintainers, m.Username)
		}

		if !slices.Equal(maintainers, wantMaintainers[p.Name]) {
			t.Errorf("maintainers of %s = %v, want %v", p.Name, maintainers, wantMaintainers[p.Name])
		}

		if p.Publisher == nil || p.Publisher.Username != "admin" {
			t.Errorf("publisher of %s = %+v, want admin", p.Name, p.Publisher)
		}

		if _, err := time.Parse(time.RFC3339, p.Date); err != nil {
			t.Errorf("date of %s = %q: %v", p.Name, p.Date, err)
		}
	}

	if !slices.Equal(got.Objects[0].Package.Keywords, []string{
		"pad",
		"string",
	}) {
		t.Errorf("keywords of %s = %v", got.Objects[0].Package.Name, got.Objects[0].Package.Keywords)
	}

	if got := search("left pad"); len(got.Objects) != 1 || got.Objects[0].Package.Name != "left-pad" {
		t.Errorf("search \"left pad\" = %+v, want left-pad", got)
	}
}
