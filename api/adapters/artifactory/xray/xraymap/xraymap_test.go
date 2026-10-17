package xraymap

import (
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		id, purl string
	}{
		{
			"npm://lodash:4.17.15",
			"pkg:npm/lodash@4.17.15",
		},
		{
			"npm://@types/node:20.1.0",
			"pkg:npm/%40types/node@20.1.0",
		},
		{
			"gav://com.fasterxml.jackson.core:jackson-databind:2.9.8",
			"pkg:maven/com.fasterxml.jackson.core/jackson-databind@2.9.8",
		},
		{
			"go://golang.org/x/text:v0.3.0",
			"pkg:golang/golang.org/x/text@v0.3.0",
		},
		{
			"go://rsc.io/quote:v1.5.2",
			"pkg:golang/rsc.io/quote@v1.5.2",
		},
		{
			"pypi://requests:2.19.0",
			"pkg:pypi/requests@2.19.0",
		},
		{
			"nuget://Newtonsoft.Json:12.0.1",
			"pkg:nuget/Newtonsoft.Json@12.0.1",
		},
		{
			"cargo://serde:1.0.0",
			"pkg:cargo/serde@1.0.0",
		},
		{
			"deb://debian:12:libc6:2.36-9",
			"pkg:deb/debian/libc6@2.36-9?distro=debian-12",
		},
		{
			"alpine://alpine:3.20.3:busybox:1.36.1-r29",
			"pkg:apk/alpine/busybox@1.36.1-r29?distro=alpine-3.20.3",
		},
		{
			"rpm://rhel:9.4:openssl:1:3.0.7-27.el9",
			"pkg:rpm/rhel/openssl@1:3.0.7-27.el9?distro=rhel-9.4",
		},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			t.Parallel()

			got, err := ToPURL(tt.id)
			if err != nil || got != tt.purl {
				t.Fatalf("ToPURL(%s) = %s, %v; want %s", tt.id, got, err, tt.purl)
			}

			back, err := FromPURL(got)
			if err != nil || back != tt.id {
				t.Errorf("FromPURL(%s) = %s, %v; want %s", got, back, err, tt.id)
			}
		})
	}
}

func TestInvalid(t *testing.T) {
	t.Parallel()

	for _, id := range []string{
		"",
		"npm://",
		"npm://x",
		"gav://a:1",
		"docker://img:1",
		"nonsense",
	} {
		if _, err := ToPURL(id); err == nil {
			t.Errorf("ToPURL(%q) succeeded", id)
		}
	}
}

func FuzzToPURL(f *testing.F) {
	for _, s := range []string{
		"npm://lodash:4.17.15",
		"gav://g:a:1",
		"go://a/b/c:v1.0.0",
		"pypi://x:1",
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, id string) {
		p, err := ToPURL(id)
		if err != nil {
			return
		}

		back, err := FromPURL(p)
		if err != nil {
			t.Fatalf("FromPURL(%q) of ToPURL(%q): %v", p, id, err)
		}

		if again, err := ToPURL(back); err != nil || again != p {
			t.Fatalf("not stable: %q -> %q -> %q -> %q (%v)", id, p, back, again, err)
		}

		if !strings.HasPrefix(p, "pkg:") {
			t.Fatalf("not a purl: %q", p)
		}
	})
}

func TestPackage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, version, purl string
	}{
		{
			"gav://org.apache:log4j",
			"",
			"pkg:maven/org.apache/log4j",
		},
		{
			"gav://org.apache:log4j",
			"2.14.1",
			"pkg:maven/org.apache/log4j@2.14.1",
		},
		{
			"npm://@types/node",
			"",
			"pkg:npm/%40types/node",
		},
		{
			"deb://debian:12:libc6",
			"",
			"pkg:deb/debian/libc6?distro=debian-12",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name+"@"+tt.version, func(t *testing.T) {
			t.Parallel()

			got, err := PackageToPURL(tt.name, tt.version)
			if err != nil || got != tt.purl {
				t.Fatalf("PackageToPURL = %q, %v; want %q", got, err, tt.purl)
			}

			name, version, err := PackageFromPURL(got)
			if err != nil || name != tt.name || version != tt.version {
				t.Errorf("PackageFromPURL(%q) = %q, %q, %v", got, name, version, err)
			}
		})
	}
}
