//go:build integration

package integration

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestAccessV2(t *testing.T) {
	t.Parallel()

	srv := server(t)
	must := func(c call, want int) string {
		t.Helper()

		resp, body := do(t, srv, c)
		if resp.StatusCode != want {
			t.Fatalf("%s %s = %d %s, want %d", c.method, c.path, resp.StatusCode, body, want)
		}

		return body
	}

	body := must(call{
		method: "POST",
		path:   "/access/api/v2/groups",
		body:   `{"name":"devs","description":"developers","auto_join":false,"admin_privileges":false}`,
	}, 201)
	if !strings.Contains(body, `"description":"developers"`) {
		t.Errorf("group = %s", body)
	}

	must(call{
		method: "POST",
		path:   "/access/api/v2/groups",
		body:   `{"name":"devs"}`,
	}, 409)

	body = must(call{
		method: "POST",
		path:   "/access/api/v2/users",
		body:   `{"username":"ada","email":"ada@example.com","password":"Ada-pass-12345","groups":["devs"],"disable_ui_access":true}`,
	}, 201)

	var u struct {
		Username        string
		Groups          []string
		Admin           bool
		DisableUIAccess bool `json:"disable_ui_access"`
		Realm           string
		Status          string
	}
	if err := json.Unmarshal([]byte(body), &u); err != nil || u.Username != "ada" || len(u.Groups) != 1 ||
		!u.DisableUIAccess || u.Realm != "internal" || u.Status != "enabled" {
		t.Errorf("user = %s %v", body, err)
	}

	must(call{
		method: "POST",
		path:   "/access/api/v2/users",
		body:   `{"username":"no-email"}`,
	}, 400)

	body = must(call{
		method: "PATCH",
		path:   "/access/api/v2/users/ada",
		body:   `{"admin":true}`,
	}, 200)
	if !strings.Contains(body, `"admin":true`) || !strings.Contains(body, `"email":"ada@example.com"`) {
		t.Errorf("patched user = %s", body)
	}

	must(call{
		method: "POST",
		path:   "/access/api/v2/groups",
		body:   `{"name":"ops"}`,
	}, 201)

	body = must(call{
		method: "PATCH",
		path:   "/access/api/v2/users/ada/groups",
		body:   `{"add":["ops"],"remove":["devs"]}`,
	}, 200)
	if !strings.Contains(body, `"groups":["ops"]`) {
		t.Errorf("user groups = %s", body)
	}

	body = must(call{
		method: "PATCH",
		path:   "/access/api/v2/groups/devs/members",
		body:   `{"add":["ada"]}`,
	}, 200)
	if !strings.Contains(body, `"members":["ada"]`) {
		t.Errorf("members = %s", body)
	}

	if body = must(call{
		method: "GET",
		path:   "/access/api/v2/users?limit=100",
	}, 200); !strings.Contains(body, `"username":"ada"`) || !strings.Contains(body, "/access/api/v2/users/ada") {
		t.Errorf("users = %s", body)
	}

	if body = must(call{
		method: "GET",
		path:   "/access/api/v2/groups",
	}, 200); !strings.Contains(body, `"group_name":"ops"`) {
		t.Errorf("groups = %s", body)
	}

	must(call{
		method: "PUT",
		path:   "/artifactory/api/repositories/libs-local",
		body:   `{"rclass":"local","packageType":"generic"}`,
	}, 200)

	body = must(call{
		method: "POST",
		path:   "/access/api/v2/permissions",
		body: `{"name":"devs-libs","resources":{"artifact":{"actions":{"groups":{"devs":["READ","WRITE"]},"users":{"ada":["READ","ANNOTATE"]}},` +
			`"targets":{"libs-local":{"include_patterns":["**"],"exclude_patterns":["secret/**"]}}}}}`,
	}, 201)
	if !strings.Contains(body, `"WRITE"`) || !strings.Contains(body, `"exclude_patterns":["secret/**"]`) {
		t.Errorf("permission = %s", body)
	}

	if body = must(call{
		method: "GET",
		path:   "/artifactory/api/v2/security/permissions/devs-libs",
	}, 200); !strings.Contains(body, `"write"`) || !strings.Contains(body, "libs-local") {
		t.Errorf("as a permission target = %s", body)
	}

	must(call{
		method: "POST",
		path:   "/access/api/v2/permissions",
		body: `{"name":"mixed","resources":{"artifact":{"actions":{"users":{"ada":["READ"]}},` +
			`"targets":{"a":{"include_patterns":["x/**"]},"b":{"include_patterns":["y/**"]}}}}}`,
	}, 400)

	must(call{
		method: "POST",
		path:   "/access/api/v2/permissions",
		body:   `{"name":"dest","resources":{"destination":{"actions":{},"targets":{}}}}`,
	}, 400)

	body = must(call{
		method: "PATCH",
		path:   "/access/api/v2/permissions/devs-libs/artifact",
		body:   `{"actions":{"groups":{"ops":["READ"]}}}`,
	}, 200)
	if !strings.Contains(body, `"ops":["READ"]`) || !strings.Contains(body, `"devs"`) {
		t.Errorf("patched resource = %s", body)
	}

	body = must(call{
		method: "PATCH",
		path:   "/access/api/v2/permissions/devs-libs/artifact",
		body:   `{"targets":{"libs-local":{"include_patterns":["app/**"]}}}`,
	}, 200)
	if !strings.Contains(body, `"libs-local":{"include_patterns":["app/**"],"exclude_patterns":["secret/**"]}`) ||
		!strings.Contains(body, `"ops":["READ"]`) {
		t.Errorf("a patch of the include patterns alone = %s, want the excludes and actions kept", body)
	}

	body = must(call{
		method: "PATCH",
		path:   "/access/api/v2/permissions/devs-libs/artifact",
		body:   `{"actions":{"users":{"ada":null}}}`,
	}, 200)
	if strings.Contains(body, `"ada"`) || !strings.Contains(body, `"devs"`) {
		t.Errorf("a patch that sets ada to null = %s, want ada removed and the groups kept", body)
	}

	must(call{
		method: "GET",
		path:   "/access/api/v2/permissions/devs-libs/build",
	}, 404)

	must(call{
		method: "PUT",
		path:   "/access/api/v2/permissions/devs-libs/build",
		body:   `{"actions":{"groups":{"devs":["READ"]}},"targets":{"artifactory-build-info":{"include_patterns":["app/**"]}}}`,
	}, 200)

	if body = must(call{
		method: "GET",
		path:   "/access/api/v2/permissions/devs-libs/build",
	}, 200); !strings.Contains(body, `"include_patterns":["app/**"]`) {
		t.Errorf("the build resource after its PUT = %s", body)
	}

	must(call{
		method: "DELETE",
		path:   "/access/api/v2/permissions/devs-libs/build",
	}, 204)

	must(call{
		method: "GET",
		path:   "/access/api/v2/permissions/devs-libs/build",
	}, 404)

	if body = must(call{
		method: "GET",
		path:   "/access/api/v2/permissions",
	}, 200); !strings.Contains(body, `"name":"devs-libs"`) {
		t.Errorf("permissions = %s", body)
	}

	must(call{
		method: "DELETE",
		path:   "/access/api/v2/permissions/devs-libs",
	}, 204)
	must(call{
		method: "DELETE",
		path:   "/access/api/v2/users/ada",
	}, 204)
	must(call{
		method: "DELETE",
		path:   "/access/api/v2/groups/ops",
	}, 204)
	must(call{
		method: "GET",
		path:   "/access/api/v2/users/ada",
	}, 404)

	must(call{
		method: "GET",
		path:   "/access/api/v2/users",
		noAuth: true,
	}, 401)

	const racers = 8

	statuses := make([]int, racers)

	var wg sync.WaitGroup
	for i := range racers {
		wg.Go(func() {
			resp, _ := do(t, srv, call{
				method: "POST",
				path:   "/access/api/v2/users",
				body: fmt.Sprintf(
					`{"username":"racer","email":"racer@example.com","password":"Racer-pass-%d"}`,
					i,
				),
			})
			statuses[i] = resp.StatusCode
		})
	}

	wg.Wait()

	winner := -1

	for i, status := range statuses {
		switch {
		case status == 201 && winner < 0:
			winner = i
		case status != 409:
			t.Errorf("concurrent create %d = %d, want one 201 and 409 for the rest (%v)", i, status, statuses)
		}
	}

	if winner < 0 {
		t.Fatalf("no concurrent create succeeded: %v", statuses)
	}

	if resp, body := do(t, srv, call{
		method: "POST",
		path:   "/access/api/v1/tokens",
		body:   `{"scope":"applied-permissions/user"}`,
		noAuth: true,
		headers: map[string]string{
			"Authorization": "Basic " + base64.StdEncoding.EncodeToString(
				fmt.Appendf(nil, "racer:Racer-pass-%d", winner),
			),
		},
	}); resp.StatusCode != 200 {
		t.Errorf("the created user's password = %d %s", resp.StatusCode, body)
	}

	for i := range racers {
		wg.Go(func() {
			resp, _ := do(t, srv, call{
				method: "POST",
				path:   "/access/api/v2/groups",
				body:   fmt.Sprintf(`{"name":"racers","description":"racer %d"}`, i),
			})
			statuses[i] = resp.StatusCode
		})
	}

	wg.Wait()

	winner = -1

	for i, status := range statuses {
		switch {
		case status == 201 && winner < 0:
			winner = i
		case status != 409:
			t.Errorf("concurrent group create %d = %d, want one 201 and 409 for the rest (%v)", i, status, statuses)
		}
	}

	if winner < 0 {
		t.Fatalf("no concurrent group create succeeded: %v", statuses)
	}

	if body := must(call{
		method: "GET",
		path:   "/access/api/v2/groups/racers",
	}, 200); !strings.Contains(body, fmt.Sprintf(`"description":"racer %d"`, winner)) {
		t.Errorf("group = %s, want the description of create %d", body, winner)
	}
}
