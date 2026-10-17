package transfer

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

var aqlWindow = regexp.MustCompile(`\.offset\((\d+)\)\.limit\((\d+)\)`)

func fakeSource(t *testing.T, sizes ...int) *httptest.Server {
	t.Helper()

	return fakeSourceOf(t, "local", sizes...)
}

func fakeSourceOf(t *testing.T, class string, sizes ...int) *httptest.Server {
	t.Helper()

	var listings atomic.Int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/artifactory/api/repositories":
			_, _ = io.WriteString(w, `[{"key":"big","type":"`+strings.ToUpper(class)+`","packageType":"generic"}]`)
		case "/artifactory/api/repositories/big":
			_, _ = io.WriteString(
				w,
				`{"key":"big","rclass":"`+class+`","packageType":"generic","members":[{"url":"https://other.example.com/artifactory/big","enabled":true}]}`,
			)
		case "/artifactory/api/security/groups", "/artifactory/api/v2/security/permissions":
			_, _ = io.WriteString(w, `[]`)
		case "/artifactory/api/search/aql":
			b, _ := io.ReadAll(r.Body)
			q := string(b)
			m := aqlWindow.FindStringSubmatch(q)
			offset, _ := strconv.Atoi(m[1])
			limit, _ := strconv.Atoi(m[2])

			listing := int(listings.Add(1)) - 1

			var items []int
			for i := sizes[0] - sizes[min(listing, len(sizes)-1)]; i < sizes[0]; i++ {
				items = append(items, i)
			}

			items = items[min(offset, len(items)):min(offset+limit, len(items))]

			results := make([]map[string]any, 0, len(items))
			for _, item := range items {
				results = append(results, map[string]any{
					"repo":   "big",
					"path":   ".",
					"name":   fmt.Sprintf("f%05d", item),
					"size":   1,
					"sha256": fmt.Sprintf("%064d", item),
				})
			}

			_ = json.NewEncoder(w).Encode(map[string]any{
				"results": results,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	return srv
}

type unreadInput struct {
	t *testing.T
}

func (u unreadInput) Read([]byte) (int, error) {
	u.t.Error("standard input was read before the destination was found")

	return 0, io.EOF
}

func TestCommandRefuses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		args          []string
		stdin         string
		noDestination bool
		want          string
	}{
		{
			name: "no credentials",
			args: []string{
				"https://artifactory.example.com",
			},
			want: "sign in to the source",
		},
		{
			name: "an empty token",
			args: []string{
				"https://artifactory.example.com",
				"--token-stdin",
			},
			want: "standard input holds no password or token",
		},
		{
			name: "an unknown output",
			args: []string{
				"https://artifactory.example.com",
				"--token-stdin",
				"-o",
				"yaml",
			},
			stdin: "token\n",
			want:  `unknown output "yaml"`,
		},
		{
			name: "no destination, before the password is read",
			args: []string{
				"https://artifactory.example.com",
				"--user",
				"reader",
				"--password-stdin",
			},
			noDestination: true,
			want:          "no destination",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var stdin io.Reader = strings.NewReader(tt.stdin)

			cmd := Command(func() (Server, error) {
				if tt.noDestination {
					return Server{}, errors.New("no destination")
				}

				return Server{}, nil
			}, func(string) {})

			if tt.noDestination {
				stdin = unreadInput{
					t: t,
				}
			}

			cmd.SetArgs(tt.args)
			cmd.SetIn(stdin)
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)

			if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestImportListsEveryFile(t *testing.T) {
	t.Parallel()

	src := fakeSource(t, 2500)
	relic := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(relic.Close)

	report, err := Import(
		t.Context(),
		Server{
			URL:    src.URL,
			Client: src.Client(),
		},
		Server{
			URL:    relic.URL,
			Client: relic.Client(),
		},
		Options{
			DryRun: true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if report.Files.Copied != 2500 {
		t.Errorf("dry run lists %d files, want 2500", report.Files.Copied)
	}
}

func TestImportReportsAListingThatChanged(t *testing.T) {
	t.Parallel()

	src := fakeSource(t, 2500, 2499)
	relic := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(relic.Close)

	report, err := Import(
		t.Context(),
		Server{
			URL:    src.URL,
			Client: src.Client(),
		},
		Server{
			URL:    relic.URL,
			Client: relic.Client(),
		},
		Options{
			DryRun: true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if !slices.ContainsFunc(report.Errors, func(e string) bool {
		return strings.Contains(e, "changed while it was listed")
	}) {
		t.Errorf("a file deleted while the listing was paged went unreported: %v", report.Errors)
	}

	if report.Files.Copied != 2500 {
		t.Errorf("the import copied %d of the 2500 files it listed", report.Files.Copied)
	}
}

func TestImportCopiesAFederatedRepositoryAsALocalOne(t *testing.T) {
	t.Parallel()

	src := fakeSourceOf(t, "federated", 3)
	relic := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(relic.Close)

	report, err := Import(
		t.Context(),
		Server{
			URL:    src.URL,
			Client: src.Client(),
		},
		Server{
			URL:    relic.URL,
			Client: relic.Client(),
		},
		Options{
			DryRun: true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if report.Files.Copied != 3 || slices.ContainsFunc(report.Errors, func(e string) bool {
		return strings.Contains(e, "federated")
	}) {
		t.Errorf("a federated repository = %d files copied, errors %v; want its 3 files, not refused",
			report.Files.Copied, report.Errors)
	}

	if !slices.ContainsFunc(report.Notes, func(n string) bool {
		return strings.Contains(n, "big") && strings.Contains(n, "federated")
	}) {
		t.Errorf("notes = %v, want one that says big was federated", report.Notes)
	}
}

func TestSourceCredentialsStayOnTheSourceHost(t *testing.T) {
	t.Parallel()

	var storageAuthorization string

	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		storageAuthorization = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, "content")
	}))
	t.Cleanup(storage.Close)

	var sourceAuthorization string

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sourceAuthorization = r.Header.Get("Authorization")
		http.Redirect(w, r, storage.URL+"/presigned/app.bin", http.StatusFound)
	}))
	t.Cleanup(source.Close)

	sourceURL, err := url.Parse(source.URL)
	if err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, source.URL+"/libs/app.bin", nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := client(credentials{
		host:  sourceURL.Host,
		token: "source-token",
	}).Do(req)
	if err != nil {
		t.Fatal(err)
	}

	_ = resp.Body.Close()

	if sourceAuthorization != "Bearer source-token" {
		t.Errorf("the source got Authorization %q, want the token", sourceAuthorization)
	}

	if storageAuthorization != "" {
		t.Errorf("the host the source redirected to got Authorization %q, want none", storageAuthorization)
	}
}
