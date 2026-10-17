// Command relic-indexer is Relic Scan's indexer for client-side scans.
// Nothing but the graph goes to stdout.
package main

import (
	"archive/tar"
	"context"
	"crypto/sha1" //nolint:gosec // SHA-1 is an identifier in the graph
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"relic/api/adapters/artifactory/xray/xraymap"

	"github.com/anchore/syft/syft"
	"github.com/anchore/syft/syft/pkg"
	_ "modernc.org/sqlite" // Syft's RPM database cataloger needs a SQLite driver
)

const exitUnsupported = 3

// Node is a node of the component graph.
type Node struct {
	ComponentID string   `json:"component_id"`
	SHA256      string   `json:"sha256,omitempty"`
	SHA1        string   `json:"sha1,omitempty"`
	Path        string   `json:"path,omitempty"`
	Licenses    []string `json:"licenses,omitempty"`
	Nodes       []*Node  `json:"nodes,omitempty"`
}

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: indexer-app version | graph <file> --temp-dir <dir>")
		return 2
	}

	switch args[0] {
	case "version":
		_, _ = fmt.Fprintln(stdout, "relic indexer-app version "+xraymap.XrayVersion)
		return 0
	case "graph":
		if len(args) < 2 {
			_, _ = fmt.Fprintln(stderr, "graph: missing file")
			return 2
		}

		g, err := graph(ctx, args[1], flagValue(args, "--temp-dir"))
		if errors.Is(err, errUnsupported) {
			_, _ = fmt.Fprintln(stderr, err)
			return exitUnsupported
		}

		if err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 1
		}

		if err := json.NewEncoder(stdout).Encode(g); err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 1
		}

		return 0
	default:
		_, _ = fmt.Fprintln(stderr, "unknown command "+args[0])
		return 2
	}
}

func flagValue(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}

		if v, ok := strings.CutPrefix(a, name+"="); ok {
			return v
		}
	}

	return ""
}

var errUnsupported = errors.New("file type not supported")

func graph(ctx context.Context, file, tempDir string) (*Node, error) {
	if tempDir != "" {
		if err := os.Setenv("TMPDIR", tempDir); err != nil {
			return nil, err
		}
	}

	info, err := os.Stat(file) //nolint:gosec // G703: the file given to the indexer
	if err != nil {
		return nil, err
	}

	if !info.Mode().IsRegular() {
		return nil, errUnsupported
	}

	sha256Hex, sha1Hex, err := hashes(file)
	if err != nil {
		return nil, err
	}

	base := filepath.Base(file)
	root := &Node{
		ComponentID: "generic://sha256:" + sha256Hex + "/" + base,
		SHA256:      sha256Hex,
		SHA1:        sha1Hex,
		Path:        base,
	}
	cfg := syft.DefaultGetSourceConfig().WithSources("file")

	image, isImage := imageArchive(file)
	if isImage {
		cfg = syft.DefaultGetSourceConfig().WithSources("docker-archive")
		root.ComponentID = "docker://" + image
	}

	src, err := syft.GetSource(ctx, file, cfg)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", file, err)
	}

	defer func() {
		_ = src.Close()
	}()

	s, err := syft.CreateSBOM(ctx, src, nil)
	if err != nil {
		return nil, fmt.Errorf("catalog %s: %w", file, err)
	}

	layers := map[string]*Node{}

	for p := range s.Artifacts.Packages.Enumerate() {
		id, err := xraymap.FromPURL(p.PURL)
		if err != nil {
			continue
		}

		n := &Node{
			ComponentID: id,
			Licenses:    licenses(p),
		}
		parent := root

		if locs := p.Locations.ToSlice(); len(locs) > 0 {
			n.Path = strings.TrimPrefix(locs[0].RealPath, "/")
			if isImage && locs[0].FileSystemID != "" {
				parent = layerNode(root, layers, locs[0].FileSystemID)
			}
		}

		if !slices.ContainsFunc(parent.Nodes, func(m *Node) bool {
			return m.ComponentID == id
		}) {
			parent.Nodes = append(parent.Nodes, n)
		}
	}

	sortNodes(root)

	return root, nil
}

func layerNode(root *Node, layers map[string]*Node, digest string) *Node {
	if n, ok := layers[digest]; ok {
		return n
	}

	hexDigest := strings.TrimPrefix(digest, "sha256:")
	n := &Node{
		ComponentID: "generic://sha256:" + hexDigest + "/sha256__" + hexDigest + ".tar",
		SHA256:      hexDigest,
	}
	layers[digest] = n
	root.Nodes = append(root.Nodes, n)

	return n
}

func sortNodes(n *Node) {
	slices.SortFunc(n.Nodes, func(a, b *Node) int {
		return strings.Compare(a.ComponentID, b.ComponentID)
	})

	for _, c := range n.Nodes {
		sortNodes(c)
	}
}

func licenses(p pkg.Package) []string {
	var out []string

	for _, l := range p.Licenses.ToSlice() {
		v := l.SPDXExpression
		if v == "" {
			v = l.Value
		}

		if v != "" && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}

	return out
}

func hashes(file string) (string, string, error) {
	f, err := os.Open(file) //nolint:gosec // the file given to the indexer
	if err != nil {
		return "", "", err
	}
	defer func() {
		_ = f.Close()
	}()

	h256, h1 := sha256.New(), sha1.New() //nolint:gosec // identifier, not security
	if _, err := io.Copy(io.MultiWriter(h256, h1), f); err != nil {
		return "", "", err
	}

	return hex.EncodeToString(h256.Sum(nil)), hex.EncodeToString(h1.Sum(nil)), nil
}

func imageArchive(file string) (string, bool) {
	f, err := os.Open(file) //nolint:gosec // the file given to the indexer
	if err != nil {
		return "", false
	}
	defer func() {
		_ = f.Close()
	}()

	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		if err != nil {
			return "", false
		}

		if hdr.Name != "manifest.json" {
			continue
		}

		var m []struct {
			RepoTags []string `json:"RepoTags"`
		}
		if json.NewDecoder(io.LimitReader(tr, 1<<20)).Decode(&m) != nil || len(m) == 0 {
			return "", false
		}

		if len(m[0].RepoTags) > 0 {
			return m[0].RepoTags[0], true
		}

		return filepath.Base(file), true
	}
}
