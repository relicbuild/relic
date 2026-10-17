package cvecatalog

import (
	"bytes"
	"compress/gzip"
	"errors"
	"strings"
	"testing"
	"time"
)

func gz(t *testing.T, s string) *bytes.Buffer {
	t.Helper()

	var b bytes.Buffer

	zw := gzip.NewWriter(&b)
	if _, err := zw.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	return &b
}

func TestReadEPSS(t *testing.T) {
	t.Parallel()

	file := "#model_version:v2026.06.15,score_date:2026-09-25T12:03:13Z\ncve,epss,percentile\n" +
		"CVE-2021-44228,0.94358,0.99962\ncve-2024-0001,0.00043,0.1\nCVE-1999-0001,0.03351,0.88224\nCVE-2000-0002,oops,0.1\n"
	keep := func(id string) bool {
		return id != "CVE-1999-0001"
	}

	scores, date, err := ReadEPSS(gz(t, file), keep)
	if err != nil {
		t.Fatal(err)
	}

	if !date.Equal(time.Date(2026, 9, 25, 12, 3, 13, 0, time.UTC)) {
		t.Errorf("date = %v", date)
	}

	if len(scores) != 2 || scores["CVE-2021-44228"] != (EPSSScore{
		0.94358,
		0.99962,
	}) ||
		scores["CVE-2024-0001"].Percentile != 0.1 {
		t.Errorf("scores = %v", scores)
	}

	for _, bad := range []string{
		"not gzip",
		"",
	} {
		if _, _, err := ReadEPSS(strings.NewReader(bad), keep); !errors.Is(err, ErrFeed) {
			t.Errorf("%q: err = %v", bad, err)
		}
	}

	for _, header := range []string{
		"id,score\nx,1\n",
		"cve,epss,percentile,model\nCVE-2021-44228,0.94358,0.99962,v3\n",
	} {
		if _, _, err := ReadEPSS(gz(t, header), keep); !errors.Is(err, ErrFeed) {
			t.Errorf("wrong header %q: err = %v", header, err)
		}
	}
}

func TestReadKEV(t *testing.T) {
	t.Parallel()

	doc := `{"catalogVersion":"2026.09.24","vulnerabilities":[
	  {"cveID":"CVE-2021-44228","dateAdded":"2021-12-10","dueDate":"2021-12-24","knownRansomwareCampaignUse":"Known","requiredAction":"Apply updates"},
	  {"cveID":"cve-2023-1","dateAdded":"2023-01-02","dueDate":"2023-01-23","knownRansomwareCampaignUse":"Unknown","requiredAction":"Patch"},
	  {"cveID":"CVE-2023-2","dateAdded":"not a date"}]}`

	entries, version, err := ReadKEV(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}

	if version != "2026.09.24" || len(entries) != 2 {
		t.Fatalf("version %q, entries %+v", version, entries)
	}

	e := entries[0]
	if e.ID != "CVE-2021-44228" || !e.Ransomware || e.Action != "Apply updates" ||
		!e.Added.Equal(
			time.Date(2021, 12, 10, 0, 0, 0, 0, time.UTC),
		) || !e.Due.Equal(time.Date(2021, 12, 24, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("entry = %+v", e)
	}

	if entries[1].ID != "CVE-2023-1" || entries[1].Ransomware {
		t.Errorf("entry = %+v", entries[1])
	}

	if _, _, err := ReadKEV(strings.NewReader(`{"catalogVersion":"x"}`)); !errors.Is(err, ErrFeed) {
		t.Errorf("no list: err = %v", err)
	}
}

const log4shell = `{"cveMetadata":{"cveId":"CVE-2021-44228","state":"PUBLISHED","datePublished":"2021-12-10T00:00:00.000Z","dateUpdated":"2025-10-21T23:25:23.121Z"},
 "containers":{
  "cna":{"providerMetadata":{"shortName":"apache"},"title":"Apache Log4j2 JNDI features",
    "descriptions":[{"lang":"de","value":"Deutsch"},{"lang":"en","value":"Apache Log4j2 2.0-beta9 through 2.15.0 ..."}],
    "metrics":[{"other":{"type":"unknown","content":{"other":"critical"}}}],
    "problemTypes":[{"descriptions":[{"cweId":"CWE-502"},{"cweId":"CWE-400"}]},{"descriptions":[{"description":"no id"}]}],
    "references":[{"url":"https://logging.apache.org/log4j/2.x/security.html"},{"url":""}]},
  "adp":[
   {"providerMetadata":{"shortName":"CVE"},"references":[{"url":"https://example.com"}]},
   {"providerMetadata":{"shortName":"CISA-ADP"},"problemTypes":[{"descriptions":[{"cweId":"CWE-917"},{"cweId":"CWE-502"}]}],
    "metrics":[{"cvssV3_1":{"baseScore":10,"vectorString":"CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:C/C:H/I:H/A:H"}},
     {"other":{"type":"ssvc","content":{"options":[{"Exploitation":"active"},{"Automatable":"yes"},{"Technical Impact":"total"}]}}},
     {"other":{"type":"kev","content":{"dateAdded":"2021-12-10"}}}]}]}}`

func TestReadRecord(t *testing.T) {
	t.Parallel()

	rec, err := ReadRecord(strings.NewReader(log4shell))
	if err != nil {
		t.Fatal(err)
	}

	if rec.ID != "CVE-2021-44228" || rec.State != "PUBLISHED" || rec.Title != "Apache Log4j2 JNDI features" ||
		!strings.HasPrefix(rec.Description, "Apache Log4j2") {
		t.Errorf("record = %+v", rec)
	}

	if rec.Published == nil || !rec.Published.Equal(time.Date(2021, 12, 10, 0, 0, 0, 0, time.UTC)) ||
		rec.Updated == nil {
		t.Errorf("dates = %v %v", rec.Published, rec.Updated)
	}

	if strings.Join(rec.CWEs, ",") != "CWE-502,CWE-400,CWE-917" || len(rec.References) != 1 {
		t.Errorf("cwes %v refs %v", rec.CWEs, rec.References)
	}

	if rec.CVSS.Score == nil || *rec.CVSS.Score != 10 || rec.CVSS.Version != "3.1" || rec.CVSS.Source != "CISA-ADP" {
		t.Errorf("cvss = %+v", rec.CVSS)
	}

	if rec.Exploitation != "active" || rec.Automatable != "yes" || rec.TechnicalImpact != "total" {
		t.Errorf("ssvc = %q %q %q", rec.Exploitation, rec.Automatable, rec.TechnicalImpact)
	}
}

func TestReadRecordPrefersTheCNAAndNewestCVSS(t *testing.T) {
	t.Parallel()

	doc := `{"cveMetadata":{"cveId":"CVE-2025-1","state":"PUBLISHED"},"containers":{
	  "cna":{"providerMetadata":{"shortName":"acme"},"metrics":[
	    {"cvssV3_1":{"baseScore":7.5,"vectorString":"CVSS:3.1/AV:N"}},{"cvssV4_0":{"baseScore":8.7,"vectorString":"CVSS:4.0/AV:N"}}]},
	  "adp":[{"providerMetadata":{"shortName":"CISA-ADP"},"metrics":[{"cvssV3_1":{"baseScore":9.8,"vectorString":"CVSS:3.1/X"}}]}]}}`

	rec, err := ReadRecord(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}

	if *rec.CVSS.Score != 8.7 || rec.CVSS.Version != "4.0" || rec.CVSS.Source != "acme" {
		t.Errorf("cvss = %+v", rec.CVSS)
	}

	if _, err := ReadRecord(strings.NewReader(`{"cveMetadata":{"cveId":"GHSA-x"}}`)); !errors.Is(err, ErrFeed) {
		t.Errorf("non-CVE: err = %v", err)
	}
}
