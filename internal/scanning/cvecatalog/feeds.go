// Package cvecatalog keeps facts about the CVEs scan findings name: the CVE record
// (description, CWEs, CVSS, CISA's SSVC decision points), the EPSS
// probability of exploitation, and whether CISA lists it as known
// exploited.
package cvecatalog

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// ErrFeed reports a feed whose content Relic cannot read.
var ErrFeed = errors.New("cve feed")

// EPSSScore is one CVE's line in the EPSS daily scores file.
type EPSSScore struct {
	Score, Percentile float64
}

// ReadEPSS reads FIRST's daily scores file (gzipped CSV, with a
// "#model_version:…,score_date:…" comment line, then "cve,epss,percentile").
func ReadEPSS(r io.Reader, keep func(id string) bool) (map[string]EPSSScore, time.Time, error) {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("%w: epss: %w", ErrFeed, err)
	}
	defer func() {
		_ = zr.Close()
	}()

	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 64<<10), 1<<20)

	out := map[string]EPSSScore{}

	var date time.Time

	header := false

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "#"):
			for field := range strings.SplitSeq(strings.TrimPrefix(line, "#"), ",") {
				if v, ok := strings.CutPrefix(field, "score_date:"); ok {
					if t, err := time.Parse(time.RFC3339, strings.Replace(v, "+0000", "Z", 1)); err == nil {
						date = t.UTC()
					}
				}
			}

			continue
		case !header:
			if line != "cve,epss,percentile" {
				return nil, time.Time{}, fmt.Errorf("%w: epss: unexpected header %q", ErrFeed, line)
			}

			header = true

			continue
		}

		id, rest, ok := strings.Cut(line, ",")
		if !ok || !keep(strings.ToUpper(id)) {
			continue
		}

		score, pct, ok := strings.Cut(rest, ",")
		s, err1 := strconv.ParseFloat(score, 64)

		p, err2 := strconv.ParseFloat(pct, 64)
		if !ok || err1 != nil || err2 != nil {
			continue
		}

		out[strings.ToUpper(id)] = EPSSScore{
			Score:      s,
			Percentile: p,
		}
	}

	if err := sc.Err(); err != nil {
		return nil, time.Time{}, fmt.Errorf("%w: epss: %w", ErrFeed, err)
	}

	if !header {
		return nil, time.Time{}, fmt.Errorf("%w: epss: no header", ErrFeed)
	}

	if date.IsZero() {
		date = time.Now().UTC()
	}

	return out, date, nil
}

// KEVEntry is a CVE in CISA's Known Exploited Vulnerabilities catalog.
type KEVEntry struct {
	ID         string
	Added, Due time.Time
	Ransomware bool
	Action     string
}

// ReadKEV reads CISA's KEV catalog (JSON).
func ReadKEV(r io.Reader) ([]KEVEntry, string, error) {
	var doc struct {
		CatalogVersion  string `json:"catalogVersion"`
		Vulnerabilities []struct {
			CVEID                      string `json:"cveID"`
			DateAdded                  string `json:"dateAdded"`
			DueDate                    string `json:"dueDate"`
			KnownRansomwareCampaignUse string `json:"knownRansomwareCampaignUse"`
			RequiredAction             string `json:"requiredAction"`
		} `json:"vulnerabilities"`
	}
	if err := json.NewDecoder(io.LimitReader(r, 64<<20)).Decode(&doc); err != nil {
		return nil, "", fmt.Errorf("%w: kev: %w", ErrFeed, err)
	}

	if doc.Vulnerabilities == nil {
		return nil, "", fmt.Errorf("%w: kev: no vulnerabilities list", ErrFeed)
	}

	out := make([]KEVEntry, 0, len(doc.Vulnerabilities))
	for _, v := range doc.Vulnerabilities {
		added, err := time.Parse(time.DateOnly, v.DateAdded)
		if err != nil || !strings.HasPrefix(strings.ToUpper(v.CVEID), "CVE-") {
			continue
		}

		due, _ := time.Parse(time.DateOnly, v.DueDate)
		out = append(out, KEVEntry{
			ID:         strings.ToUpper(v.CVEID),
			Added:      added,
			Due:        due,
			Ransomware: strings.EqualFold(v.KnownRansomwareCampaignUse, "Known"),
			Action:     v.RequiredAction,
		})
	}

	return out, doc.CatalogVersion, nil
}

// Record is what Relic keeps from a CVE record (CVE JSON 5).
type Record struct {
	ID, Title, Description, State string
	Published, Updated            *time.Time
	CWEs, References              []string
	CVSS                          CVSS
	// CISA's SSVC decision points, from its ADP container.
	Exploitation, Automatable, TechnicalImpact string
}

// CVSS is the base score a record carries, with who scored it.
type CVSS struct {
	Score   *float64
	Vector  string
	Version string
	Source  string // the CNA's or an ADP's short name
}

type cvssMetric struct {
	BaseScore    float64 `json:"baseScore"`
	VectorString string  `json:"vectorString"`
}

type metrics []map[string]json.RawMessage

type container struct {
	ProviderMetadata struct {
		ShortName string `json:"shortName"`
	} `json:"providerMetadata"`
	Title        string `json:"title"`
	Descriptions []struct {
		Lang  string `json:"lang"`
		Value string `json:"value"`
	} `json:"descriptions"`
	Metrics      metrics `json:"metrics"`
	ProblemTypes []struct {
		Descriptions []struct {
			CWEID string `json:"cweId"`
		} `json:"descriptions"`
	} `json:"problemTypes"`
	References []struct {
		URL string `json:"url"`
	} `json:"references"`
}

var cvssOrder = []struct {
	key, version string
}{
	{
		"cvssV4_0",
		"4.0",
	},
	{
		"cvssV3_1",
		"3.1",
	},
	{
		"cvssV3_0",
		"3.0",
	},
	{
		"cvssV2_0",
		"2.0",
	},
}

func (m metrics) best() (CVSS, bool) {
	for _, o := range cvssOrder {
		for _, entry := range m {
			raw, ok := entry[o.key]
			if !ok {
				continue
			}

			var c cvssMetric
			if json.Unmarshal(raw, &c) == nil && c.VectorString != "" {
				score := c.BaseScore

				return CVSS{
					Score:   &score,
					Vector:  c.VectorString,
					Version: o.version,
				}, true
			}
		}
	}

	return CVSS{}, false
}

// ReadRecord reads a CVE JSON 5 record.
func ReadRecord(r io.Reader) (Record, error) {
	var doc struct {
		CVEMetadata struct {
			CVEID         string `json:"cveId"`
			State         string `json:"state"`
			DatePublished string `json:"datePublished"`
			DateUpdated   string `json:"dateUpdated"`
		} `json:"cveMetadata"`
		Containers struct {
			CNA container   `json:"cna"`
			ADP []container `json:"adp"`
		} `json:"containers"`
	}
	if err := json.NewDecoder(io.LimitReader(r, 16<<20)).Decode(&doc); err != nil {
		return Record{}, fmt.Errorf("%w: cve record: %w", ErrFeed, err)
	}

	m := doc.CVEMetadata
	if !strings.HasPrefix(strings.ToUpper(m.CVEID), "CVE-") {
		return Record{}, fmt.Errorf("%w: cve record without a CVE ID", ErrFeed)
	}

	cna := doc.Containers.CNA

	rec := Record{
		ID:         strings.ToUpper(m.CVEID),
		Title:      cna.Title,
		State:      m.State,
		Published:  parseTime(m.DatePublished),
		Updated:    parseTime(m.DateUpdated),
		CWEs:       []string{},
		References: []string{},
	}
	for _, d := range cna.Descriptions {
		if rec.Description == "" || strings.HasPrefix(strings.ToLower(d.Lang), "en") {
			rec.Description = d.Value
			if strings.HasPrefix(strings.ToLower(d.Lang), "en") {
				break
			}
		}
	}

	seen := map[string]bool{}

	for _, c := range append([]container{
		cna,
	}, doc.Containers.ADP...) {
		for _, pt := range c.ProblemTypes {
			for _, d := range pt.Descriptions {
				if strings.HasPrefix(d.CWEID, "CWE-") && !seen[d.CWEID] {
					seen[d.CWEID] = true
					rec.CWEs = append(rec.CWEs, d.CWEID)
				}
			}
		}
	}

	for _, ref := range cna.References {
		if ref.URL != "" && len(rec.References) < 50 {
			rec.References = append(rec.References, ref.URL)
		}
	}

	if c, ok := cna.Metrics.best(); ok {
		c.Source = cna.ProviderMetadata.ShortName
		rec.CVSS = c
	}

	for _, adp := range doc.Containers.ADP {
		if rec.CVSS.Vector == "" {
			if c, ok := adp.Metrics.best(); ok {
				c.Source = adp.ProviderMetadata.ShortName
				rec.CVSS = c
			}
		}

		rec.readSSVC(adp.Metrics)
	}

	return rec, nil
}

func (rec *Record) readSSVC(m metrics) {
	for _, entry := range m {
		raw, ok := entry["other"]
		if !ok {
			continue
		}

		var other struct {
			Type    string `json:"type"`
			Content struct {
				Options []map[string]string `json:"options"`
			} `json:"content"`
		}
		if json.Unmarshal(raw, &other) != nil || !strings.EqualFold(other.Type, "ssvc") {
			continue
		}

		for _, opt := range other.Content.Options {
			for k, v := range opt {
				switch strings.ToLower(k) {
				case "exploitation":
					rec.Exploitation = strings.ToLower(v)
				case "automatable":
					rec.Automatable = strings.ToLower(v)
				case "technical impact":
					rec.TechnicalImpact = strings.ToLower(v)
				}
			}
		}
	}
}

func parseTime(s string) *time.Time {
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05",
		time.DateOnly,
	} {
		if t, err := time.Parse(layout, s); err == nil {
			t = t.UTC()
			return &t
		}
	}

	return nil
}
