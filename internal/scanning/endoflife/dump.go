// Package endoflife flags components past or near their end of life, from
// endoflife.date's product data, and deprecated or unmaintained ones, from
// deps.dev when an operator turns it on.
package endoflife

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/package-url/packageurl-go"
)

// Product is an endoflife.date product: the package URLs that name it and
// its release cycles.
type Product struct {
	Name   string
	Label  string
	PURLs  []string
	Cycles []Cycle
}

// Cycle is a product's release cycle.
type Cycle struct {
	Name string
	// EOLFrom is the ISO date the cycle reaches end of life, "" when the
	// product announces none.
	EOLFrom      string
	IsEOL        bool
	IsMaintained bool
	Latest       string
}

type dump struct {
	Result []struct {
		Name        string `json:"name"`
		Label       string `json:"label"`
		Identifiers []struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		} `json:"identifiers"`
		Releases []struct {
			Name         string  `json:"name"`
			EOLFrom      *string `json:"eolFrom"`
			IsEOL        bool    `json:"isEol"`
			IsMaintained bool    `json:"isMaintained"`
			Latest       *struct {
				Name string `json:"name"`
			} `json:"latest"`
		} `json:"releases"`
	} `json:"result"`
}

const maxDump = 64 << 20

// ErrInvalidDump is returned for a dump that does not read as end-of-life data.
var ErrInvalidDump = errors.New("invalid end-of-life data")

// ParseDump reads endoflife.date's full product dump (/api/v1/products/full).
func ParseDump(r io.Reader) ([]Product, error) {
	var d dump

	limited := &io.LimitedReader{
		R: r,
		N: maxDump + 1,
	}
	if err := json.NewDecoder(limited).Decode(&d); err != nil {
		if limited.N == 0 {
			return nil, fmt.Errorf("%w: larger than %d MiB", ErrInvalidDump, maxDump>>20)
		}

		return nil, fmt.Errorf("%w: %w", ErrInvalidDump, err)
	}

	out := make([]Product, 0, len(d.Result))

	for _, p := range d.Result {
		if p.Name == "" {
			continue
		}

		prod := Product{
			Name:  p.Name,
			Label: p.Label,
		}

		for _, id := range p.Identifiers {
			if id.Type != "purl" {
				continue
			}

			if key, ok := Key(id.ID); ok {
				prod.PURLs = append(prod.PURLs, key)
			}
		}

		for _, rel := range p.Releases {
			if rel.Name == "" {
				continue
			}

			c := Cycle{
				Name:         rel.Name,
				IsEOL:        rel.IsEOL,
				IsMaintained: rel.IsMaintained,
			}
			if rel.EOLFrom != nil {
				c.EOLFrom = *rel.EOLFrom
			}

			if rel.Latest != nil {
				c.Latest = rel.Latest.Name
			}

			prod.Cycles = append(prod.Cycles, c)
		}

		out = append(out, prod)
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("%w: no products", ErrInvalidDump)
	}

	return out, nil
}

// Key is a package URL without its version, qualifiers and subpath, with
// its type, namespace and name lower-cased: what end-of-life data and a
// component's package URL have in common.
func Key(purl string) (string, bool) {
	p, err := packageurl.FromString(purl)
	if err != nil || p.Type == "" || p.Name == "" {
		return "", false
	}

	return packageurl.NewPackageURL(
		strings.ToLower(p.Type),
		strings.ToLower(p.Namespace),
		strings.ToLower(p.Name),
		"",
		nil,
		"",
	).ToString(), true
}
