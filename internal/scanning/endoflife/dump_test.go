package endoflife

import (
	"io"
	"strings"
	"testing"
	"time"
)

const sampleDump = `{"schema_version":"1.2.1","total":2,"result":[
 {"name":"django","label":"Django","identifiers":[{"type":"purl","id":"pkg:pypi/Django"},{"type":"repology","id":"django"}],
  "releases":[
   {"name":"5.2","eolFrom":"2028-04-30","isEol":false,"isMaintained":true,"latest":{"name":"5.2.7"}},
   {"name":"4.2","eolFrom":"2026-04-30","isEol":true,"isMaintained":false,"latest":{"name":"4.2.25"}}]},
 {"name":"adonisjs","label":"AdonisJS","identifiers":[{"type":"purl","id":"pkg:npm/%40adonisjs/core"}],
  "releases":[{"name":"7","eolFrom":null,"isEol":false,"isMaintained":true,"latest":null}]}]}`

func TestParseDump(t *testing.T) {
	t.Parallel()

	products, err := ParseDump(strings.NewReader(sampleDump))
	if err != nil {
		t.Fatal(err)
	}

	if len(products) != 2 {
		t.Fatalf("products = %d, want 2", len(products))
	}

	django := products[0]
	if django.Label != "Django" || len(django.PURLs) != 1 || django.PURLs[0] != "pkg:pypi/django" {
		t.Errorf("django = %+v", django)
	}

	if c := django.Cycles[1]; c.Name != "4.2" || c.EOLFrom != "2026-04-30" || !c.IsEOL || c.Latest != "4.2.25" {
		t.Errorf("django 4.2 = %+v", c)
	}

	if got := products[1].PURLs[0]; got != "pkg:npm/%40adonisjs/core" {
		t.Errorf("adonisjs purl = %q", got)
	}

	if c := products[1].Cycles[0]; c.EOLFrom != "" || c.Latest != "" {
		t.Errorf("adonisjs 7 = %+v", c)
	}

	if _, err := ParseDump(strings.NewReader(`{"result":[]}`)); err == nil {
		t.Error("an empty dump parsed")
	}

	oversized := io.MultiReader(
		strings.NewReader(`{"result":[{"name":"`),
		strings.NewReader(strings.Repeat("a", maxDump)),
	)
	if _, err := ParseDump(oversized); err == nil || !strings.Contains(err.Error(), "64 MiB") {
		t.Errorf("oversized dump: err = %v, want the size limit named", err)
	}
}

func TestKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, purl, want string
	}{
		{
			"version and qualifiers dropped",
			"pkg:deb/debian/OpenSSL@3.0.11-1?arch=amd64&distro=debian-12",
			"pkg:deb/debian/openssl",
		},
		{
			"encoded namespace kept",
			"pkg:npm/%40adonisjs/core@7.1.0",
			"pkg:npm/%40adonisjs/core",
		},
		{
			"no namespace",
			"pkg:pypi/Django@4.2.1",
			"pkg:pypi/django",
		},
		{
			"not a package URL",
			"django",
			"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, _ := Key(tt.purl)
			if got != tt.want {
				t.Errorf("Key(%q) = %q, want %q", tt.purl, got, tt.want)
			}
		})
	}
}

func TestInCycle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		version, cycle string
		want           bool
	}{
		{
			"1.27.1",
			"1.27",
			true,
		},
		{
			"1.27",
			"1.27",
			true,
		},
		{
			"1.270.0",
			"1.27",
			false,
		},
		{
			"v3.12.4",
			"3.12",
			true,
		},
		{
			"1:3.0.11-1~deb12u2",
			"3.0",
			true,
		},
		{
			"3.17.0",
			"3.17",
			true,
		},
		{
			"12",
			"12",
			true,
		},
		{
			"12.5",
			"1",
			false,
		},
		{
			"4.2rc1",
			"4.2",
			false,
		},
	}
	for _, tt := range tests {
		if got := inCycle(tt.version, tt.cycle); got != tt.want {
			t.Errorf("inCycle(%q, %q) = %v, want %v", tt.version, tt.cycle, got, tt.want)
		}
	}
}

func TestCycleRisk(t *testing.T) {
	t.Parallel()

	c := &Catalog{
		now: func() time.Time {
			return time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
		},
	}

	tests := []struct {
		name     string
		eolFrom  string
		isEOL    bool
		want     string
		wantRisk bool
	}{
		{
			"past its date",
			"2026-04-30",
			false,
			SeverityHigh,
			true,
		},
		{
			"marked end of life without a date",
			"",
			true,
			SeverityHigh,
			true,
		},
		{
			"within 90 days",
			"2026-12-01",
			false,
			SeverityLow,
			true,
		},
		{
			"on the day itself",
			"2026-10-06",
			false,
			SeverityHigh,
			true,
		},
		{
			"exactly 90 days out",
			"2027-01-04",
			false,
			SeverityLow,
			true,
		},
		{
			"91 days out",
			"2027-01-05",
			false,
			"",
			false,
		},
		{
			"far off",
			"2028-04-30",
			false,
			"",
			false,
		},
		{
			"no date, supported",
			"",
			false,
			"",
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r, ok := c.cycleRisk("Django", "4.2", tt.eolFrom, tt.isEOL)
			if ok != tt.wantRisk || r.Severity != tt.want {
				t.Errorf("cycleRisk = %+v, %v; want severity %q, %v", r, ok, tt.want, tt.wantRisk)
			}

			if ok && r.Kind != KindEndOfLife {
				t.Errorf("kind = %q", r.Kind)
			}
		})
	}
}
