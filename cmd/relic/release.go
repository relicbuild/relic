package main

import (
	"archive/zip"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/delivery/evidence"

	"relic/internal/platform/terminal"
)

func releaseCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "release",
		Short: "Create, promote, export and import releases: signed, immutable sets of artifacts",
		Long: "A release version takes its artifacts from files, build runs, other releases or a query, " +
			"is signed when completed, and is promoted through environments (the repositories whose " +
			"environments setting names the environment).",
	}
	cmd.AddCommand(
		releaseCreateCmd(g, false),
		releaseCreateCmd(g, true),
		releaseFinalizeCmd(g),
		releaseListCmd(g),
		releaseGetCmd(g),
		releasePromoteCmd(g),
		releaseTagCmd(g),
		releaseExportCmd(g),
		releaseImportCmd(g),
		releasePublicKeyCmd(g),
		releaseTrustKeyCmd(g),
		releaseDeleteCmd(g),
	)

	return cmd
}

type releaseSources struct {
	files, builds, releases []string
	withDependencies        bool
	specFile                string
}

func (r *releaseSources) register(cmd *cobra.Command) {
	cmd.Flags().StringArrayVar(&r.files, "file", nil, "a file, <repository>/<path> (repeatable)")
	cmd.Flags().StringArrayVar(&r.builds, "build", nil, "a build run's artifacts, <name>/<number> (repeatable)")
	cmd.Flags().BoolVar(&r.withDependencies, "dependencies", false, "take the build runs' dependencies too")
	cmd.Flags().
		StringArrayVar(&r.releases, "release", nil, "another release's artifacts, <name>/<version> (repeatable)")
	cmd.Flags().
		StringVarP(&r.specFile, "filename", "f", "", "sources in a YAML or JSON file: a list of ReleaseSource (- for standard input)")
}

func (r *releaseSources) read(cmd *cobra.Command) ([]*relicv1.ReleaseSource, error) {
	var out []*relicv1.ReleaseSource

	if len(r.files) > 0 {
		s := &relicv1.ReleaseSource{}
		for _, f := range r.files {
			s.Files = append(s.Files, &relicv1.ReleaseFileRef{
				Path: strings.Trim(f, "/"),
			})
		}

		out = append(out, s)
	}

	if len(r.builds) > 0 {
		s := &relicv1.ReleaseSource{}

		for _, b := range r.builds {
			name, number, err := parseBuildRef(b)
			if err != nil {
				return nil, err
			}

			s.Builds = append(s.Builds, &relicv1.ReleaseBuildRef{
				Name:         name,
				Number:       number,
				Dependencies: r.withDependencies,
			})
		}

		out = append(out, s)
	}

	if len(r.releases) > 0 {
		s := &relicv1.ReleaseSource{}

		for _, v := range r.releases {
			name, version, ok := strings.Cut(v, "/")
			if !ok || name == "" || version == "" {
				return nil, fmt.Errorf("--release %q is not <name>/<version>", v)
			}

			s.Releases = append(s.Releases, &relicv1.ReleaseRef{
				Name:    name,
				Version: version,
			})
		}

		out = append(out, s)
	}

	if r.specFile != "" {
		var (
			b   []byte
			err error
		)

		if r.specFile == "-" {
			b, err = io.ReadAll(cmd.InOrStdin())
		} else {
			b, err = os.ReadFile(r.specFile)
		}

		if err != nil {
			return nil, err
		}

		sources, err := decodeSources(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", r.specFile, err)
		}

		out = append(out, sources...)
	}

	if len(out) == 0 {
		return nil, errors.New("give the release's artifacts: --file, --build, --release or -f")
	}

	return out, nil
}

func decodeSources(b []byte) ([]*relicv1.ReleaseSource, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, err
	}

	if len(doc.Content) == 0 {
		return nil, nil
	}

	if doc.Content[0].Kind == yaml.SequenceNode {
		for _, item := range doc.Content[0].Content {
			tagStringFields(item, (&relicv1.ReleaseSource{}).ProtoReflect().Descriptor())
		}
	}

	var list []any
	if err := doc.Decode(&list); err != nil {
		return nil, err
	}

	var out []*relicv1.ReleaseSource

	for i, item := range list {
		j, err := json.Marshal(item)
		if err != nil {
			return nil, err
		}

		s := &relicv1.ReleaseSource{}
		if err := protojson.Unmarshal(j, s); err != nil {
			return nil, fmt.Errorf("source %d: %w", i+1, err)
		}

		out = append(out, s)
	}

	return out, nil
}

func tagStringFields(n *yaml.Node, md protoreflect.MessageDescriptor) {
	if n.Kind != yaml.MappingNode {
		return
	}

	fields := md.Fields()

	for i := 0; i+1 < len(n.Content); i += 2 {
		key, value := n.Content[i].Value, n.Content[i+1]

		fd := fields.ByJSONName(key)
		if fd == nil {
			fd = fields.ByName(protoreflect.Name(key))
		}

		if fd == nil || fd.IsMap() {
			continue
		}

		values := []*yaml.Node{
			value,
		}
		if value.Kind == yaml.SequenceNode {
			values = value.Content
		}

		for _, v := range values {
			switch {
			case fd.Kind() == protoreflect.StringKind && v.Kind == yaml.ScalarNode && v.Tag != "!!null":
				v.Tag = "!!str"
			case fd.Kind() == protoreflect.MessageKind:
				tagStringFields(v, fd.Message())
			}
		}
	}
}

func printRelease(w io.Writer, r *relicv1.Release) {
	status := strings.TrimPrefix(strings.ToLower(r.GetStatus().String()), "release_status_")
	tag := ""

	if r.GetTag() != "" {
		tag = " tag " + r.GetTag()
	}

	_, _ = fmt.Fprintf(w, "%s/%s %s%s, %d artifacts\n", r.GetName(), r.GetVersion(), status, tag, len(r.GetArtifacts()))
}

func releaseCreateCmd(g *globals, update bool) *cobra.Command {
	var (
		project string
		draft   bool
		sources releaseSources
		output  string
	)

	use, short := "create", "Create a release version"
	if update {
		use, short = "update", "Add artifacts to a draft release version"
	}

	cmd := &cobra.Command{
		Use:   use + " <name> <version>",
		Short: short,
		Long: short + ". Artifacts come from --file, --build and --release, or from -f, a file listing " +
			"sources (ReleaseSource: files, builds, releases or an item query), which is how a " +
			"pipeline keeps its release definition in the repository.",
		Example: fmt.Sprintf(
			"  relic release %[1]s app 1.4.0 --build app/42 --file libs-local/docs/guide.pdf\n"+
				"  relic release %[1]s app 1.4.0 -f release.yaml",
			use,
		),
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.dial()
			if err != nil {
				return err
			}

			srcs, err := sources.read(cmd)
			if err != nil {
				return err
			}

			spec := &relicv1.ReleaseSpec{
				Name:    args[0],
				Version: args[1],
				Sources: srcs,
				Draft:   draft,
			}

			var r *relicv1.Release

			if update {
				resp, err := c.releases.UpdateRelease(
					cmd.Context(),
					&relicv1.UpdateReleaseRequest{
						Project: project,
						Spec:    spec,
					},
				)
				if err != nil {
					return fmt.Errorf("release update: %w", err)
				}

				r = resp.GetRelease()
			} else {
				resp, err := c.releases.CreateRelease(
					cmd.Context(),
					&relicv1.CreateReleaseRequest{
						Project: project,
						Spec:    spec,
					},
				)
				if err != nil {
					return fmt.Errorf("release create: %w", err)
				}

				r = resp.GetRelease()
			}

			return printMessage(cmd.OutOrStdout(), output, r, func(w io.Writer) error {
				printRelease(w, r)

				return nil
			})
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "project the release belongs to")

	if !update {
		cmd.Flags().BoolVar(&draft, "draft", false, "create a draft that release update adds to until release finalize")
	}

	sources.register(cmd)
	outputFlag(cmd, &output)

	return cmd
}

type releaseRef struct {
	project string
}

func (r *releaseRef) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&r.project, "project", "", "project the release belongs to")
}

func releaseFinalizeCmd(g *globals) *cobra.Command {
	var ref releaseRef

	cmd := &cobra.Command{
		Use:   "finalize <name> <version>",
		Short: "Complete a draft release version, signing it",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.dial()
			if err != nil {
				return err
			}

			r, err := c.releases.FinalizeRelease(
				cmd.Context(),
				&relicv1.FinalizeReleaseRequest{
					Project: ref.project,
					Name:    args[0],
					Version: args[1],
				},
			)
			if err != nil {
				return fmt.Errorf("release finalize: %w", err)
			}

			printRelease(cmd.OutOrStdout(), r.GetRelease())

			return nil
		},
	}
	ref.register(cmd)

	return cmd
}

func releaseListCmd(g *globals) *cobra.Command {
	var (
		ref    releaseRef
		output string
	)

	cmd := &cobra.Command{
		Use:     "list [<name>]",
		Short:   "List releases, or one release's versions",
		Example: "  relic release list\n  relic release list app -o json",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.dial()
			if err != nil {
				return err
			}

			if len(args) == 0 {
				req := &relicv1.ListReleasesRequest{}
				if cmd.Flags().Changed("project") {
					req.Project = &ref.project
				}

				r, err := c.releases.ListReleases(cmd.Context(), req)
				if err != nil {
					return fmt.Errorf("release list: %w", err)
				}

				return printMessage(cmd.OutOrStdout(), output, r, func(w io.Writer) error {
					table := terminal.Table{
						Headers: []string{
							"NAME",
							"PROJECT",
							"LATEST",
							"VERSIONS",
						},
						RightAligned: []int{
							3,
						},
					}

					for _, rel := range r.GetReleases() {
						table.Rows = append(table.Rows, []string{
							rel.GetName(),
							cmp.Or(rel.GetProject(), "default"),
							rel.GetVersion(),
							strconv.FormatInt(int64(rel.GetVersions()), 10),
						})
					}

					return table.Print(w)
				})
			}

			r, err := c.releases.ListReleaseVersions(
				cmd.Context(),
				&relicv1.ListReleaseVersionsRequest{
					Project: ref.project,
					Name:    args[0],
				},
			)
			if err != nil {
				return fmt.Errorf("release list %s: %w", args[0], err)
			}

			return printMessage(cmd.OutOrStdout(), output, r, func(w io.Writer) error {
				table := terminal.Table{
					Headers: []string{
						"VERSION",
						"STATUS",
						"TAG",
						"ARTIFACTS",
						"CREATED",
						"BY",
					},
					RightAligned: []int{
						3,
					},
				}

				for _, v := range r.GetReleases() {
					table.Rows = append(table.Rows, []string{
						v.GetVersion(),
						strings.TrimPrefix(strings.ToLower(v.GetStatus().String()), "release_status_"),
						v.GetTag(),
						strconv.Itoa(len(v.GetArtifacts())),
						localTime(v.GetCreated(), time.DateTime),
						v.GetCreatedBy(),
					})
				}

				return table.Print(w)
			})
		},
	}
	ref.register(cmd)
	outputFlag(cmd, &output)

	return cmd
}

func releaseGetCmd(g *globals) *cobra.Command {
	var (
		ref    releaseRef
		output string
	)

	cmd := &cobra.Command{
		Use:     "get <name> <version>",
		Short:   "Show a release version and its artifacts",
		Example: "  relic release get app 1.4.0 -o yaml",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.dial()
			if err != nil {
				return err
			}

			r, err := c.releases.GetRelease(
				cmd.Context(),
				&relicv1.GetReleaseRequest{
					Project: ref.project,
					Name:    args[0],
					Version: args[1],
				},
			)
			if err != nil {
				return fmt.Errorf("release get: %w", err)
			}

			rel := r.GetRelease()

			return printMessage(cmd.OutOrStdout(), output, rel, func(w io.Writer) error {
				printRelease(w, rel)

				table := terminal.Table{
					Headers: []string{
						"ARTIFACT",
						"SIZE",
					},
					RightAligned: []int{
						1,
					},
				}

				for _, a := range rel.GetArtifacts() {
					table.Rows = append(table.Rows, []string{
						a.GetRepository() + "/" + a.GetPath(),
						terminal.ByteSize(a.GetSize()),
					})
				}

				return table.Print(w)
			})
		},
	}
	ref.register(cmd)
	outputFlag(cmd, &output)

	return cmd
}

func releasePromoteCmd(g *globals) *cobra.Command {
	var (
		ref              releaseRef
		include, exclude []string
		move             bool
		output           string
	)

	cmd := &cobra.Command{
		Use:   "promote <name> <version> <environment>",
		Short: "Promote a release version into an environment's repositories",
		Long: "Copies each of the version's artifacts into one of the local repositories whose environments " +
			"setting names the environment: the only one, or else the first by name of the artifact's " +
			"source format, or else the first generic one. A promotion always copies; --move is accepted " +
			"and performed as a copy.",
		Example: "  relic release promote app 1.4.0 PROD\n  relic release promote app 1.4.0 QA --include qa-generic",
		Args:    cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.dial()
			if err != nil {
				return err
			}

			r, err := c.releases.PromoteRelease(
				cmd.Context(),
				&relicv1.PromoteReleaseRequest{
					Project:     ref.project,
					Name:        args[0],
					Version:     args[1],
					Environment: args[2],
					Include:     include,
					Exclude:     exclude,
					Move:        move,
				},
			)
			if err != nil {
				return fmt.Errorf("release promote: %w", err)
			}

			return printMessage(cmd.OutOrStdout(), output, r, func(w io.Writer) error {
				if _, err := fmt.Fprintf(w, "%s/%s promoted to %s\n", args[0], args[1], args[2]); err != nil {
					return err
				}

				for _, m := range r.GetPromotion().GetMessages() {
					if _, err := fmt.Fprintf(w, "  %s\n", m); err != nil {
						return err
					}
				}

				return nil
			})
		},
	}
	ref.register(cmd)
	cmd.Flags().StringSliceVar(&include, "include", nil, "promote into these repositories only")
	cmd.Flags().StringSliceVar(&exclude, "exclude", nil, "leave these repositories out")
	cmd.Flags().BoolVar(&move, "move", false, "ask for a move; the server performs it as a copy")
	outputFlag(cmd, &output)

	return cmd
}

func releaseTagCmd(g *globals) *cobra.Command {
	var ref releaseRef

	cmd := &cobra.Command{
		Use:     "tag <name> <version> [<tag>]",
		Short:   "Tag a release version, or clear its tag",
		Example: "  relic release tag app 1.4.0 stable\n  relic release tag app 1.4.0      # clears the tag",
		Args:    cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			tag := ""
			if len(args) == 3 {
				tag = args[2]
			}

			c, err := g.dial()
			if err != nil {
				return err
			}

			if _, err := c.releases.SetReleaseTag(
				cmd.Context(),
				&relicv1.SetReleaseTagRequest{
					Project: ref.project,
					Name:    args[0],
					Version: args[1],
					Tag:     tag,
				},
			); err != nil {
				return fmt.Errorf("release tag: %w", err)
			}

			if tag == "" {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s/%s tag cleared\n", args[0], args[1])
			} else {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s/%s tagged %s\n", args[0], args[1], tag)
			}

			return nil
		},
	}
	ref.register(cmd)

	return cmd
}

func releaseExportCmd(g *globals) *cobra.Command {
	var (
		ref  releaseRef
		file string
	)

	cmd := &cobra.Command{
		Use:     "export <name> <version>",
		Short:   "Export a release version as an archive: its artifacts and signed manifest",
		Long:    "Writes the archive to the release's repository and downloads it (default: its own name, in the current directory).",
		Example: "  relic release export app 1.4.0\n  relic release export app 1.4.0 --file /media/transfer/app-1.4.0.zip",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.dial()
			if err != nil {
				return err
			}

			r, err := c.releases.ExportRelease(
				cmd.Context(),
				&relicv1.ExportReleaseRequest{
					Project: ref.project,
					Name:    args[0],
					Version: args[1],
				},
			)
			if err != nil {
				return fmt.Errorf("release export: %w", err)
			}

			a := r.GetArchive()
			if file == "" {
				file = path.Base(a.GetPath())
			}

			req, err := http.NewRequestWithContext(
				cmd.Context(),
				http.MethodGet,
				c.fileURL(a.GetRepository(), a.GetPath()),
				nil,
			)
			if err != nil {
				return err
			}

			resp, err := c.http.Do(req)
			if err != nil {
				return err
			}

			defer func() {
				_ = resp.Body.Close()
			}()

			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("release export: download: %w", httpError(resp))
			}

			if err := writeFileAtomic(file, resp.Body, 0o644); err != nil {
				return fmt.Errorf("release export: %w", err)
			}

			fingerprint, err := archiveKeyFingerprint(file)
			if err != nil {
				return fmt.Errorf("release export: %w", err)
			}

			_, _ = fmt.Fprintf(
				cmd.OutOrStdout(),
				"%s/%s exported to %s\nsigning key fingerprint %s\n",
				args[0],
				args[1],
				file,
				fingerprint,
			)

			return nil
		},
	}
	ref.register(cmd)
	cmd.Flags().StringVarP(&file, "file", "o", "", "where to write the archive")

	return cmd
}

func writeFileAtomic(name string, r io.Reader, mode fs.FileMode) error {
	if target, err := filepath.EvalSymlinks(name); err == nil {
		name = target
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(name), ".relic-*")
	if err != nil {
		return err
	}

	defer func() {
		_ = os.Remove(tmp.Name())
	}()

	if _, err := io.Copy(tmp, r); err != nil {
		_ = tmp.Close()

		return err
	}

	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()

		return err
	}

	if err := tmp.Close(); err != nil {
		return err
	}

	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}

	return os.Rename(tmp.Name(), name)
}

func releaseImportCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "import <archive>",
		Short: "Import a release archive written by release export (administrators)",
		Long: "The archive's manifest must be signed by this server's release key or by a trusted key: " +
			"trust the exporting server's key first with release trust-key.",
		Example: "  relic release import app-1.4.0.zip",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fingerprint, err := archiveKeyFingerprint(args[0])
			if err != nil {
				return fmt.Errorf("release import: %w", err)
			}

			f, err := os.Open(args[0])
			if err != nil {
				return err
			}

			defer func() {
				_ = f.Close()
			}()

			st, err := f.Stat()
			if err != nil {
				return err
			}

			c, err := g.dial()
			if err != nil {
				return err
			}

			req, err := http.NewRequestWithContext(cmd.Context(), http.MethodPost, c.url+"/api/v1/releases/import", f)
			if err != nil {
				return err
			}

			req.ContentLength = st.Size()
			req.Header.Set("Content-Type", "application/octet-stream")

			resp, err := c.http.Do(req)
			if err != nil {
				return err
			}

			defer func() {
				_ = resp.Body.Close()
			}()

			if resp.StatusCode/100 != 2 {
				return fmt.Errorf("release import: %w", httpError(resp))
			}

			got := &relicv1.GetReleaseResponse{}

			body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			if err != nil {
				return err
			}

			if err := (protojson.UnmarshalOptions{
				DiscardUnknown: true,
			}).Unmarshal(body, got); err != nil {
				return fmt.Errorf("release import: read the answer: %w", err)
			}

			_, _ = fmt.Fprintf(
				cmd.OutOrStdout(),
				"%s/%s imported\nsigning key fingerprint %s\n",
				got.GetRelease().GetName(),
				got.GetRelease().GetVersion(),
				fingerprint,
			)

			return nil
		},
	}
}

const (
	releaseSigningKey    = "release-bundles"
	releaseArchiveKey    = "signing-key.pem"
	maxReleaseArchiveKey = 1 << 20
)

// archiveKeyFingerprint is the fingerprint of the signing key a release
// archive carries, as the server's trusted keys list it.
func archiveKeyFingerprint(name string) (string, error) {
	zr, err := zip.OpenReader(name)
	if err != nil {
		return "", fmt.Errorf("not a release archive: %w", err)
	}

	defer func() {
		_ = zr.Close()
	}()

	var key *zip.File

	for _, f := range zr.File {
		if f.Name != releaseArchiveKey {
			continue
		}

		if key != nil {
			return "", fmt.Errorf("the archive has two %s entries", releaseArchiveKey)
		}

		key = f
	}

	if key == nil {
		return "", fmt.Errorf("the archive has no %s", releaseArchiveKey)
	}

	rc, err := key.Open()
	if err != nil {
		return "", fmt.Errorf("%s: %w", releaseArchiveKey, err)
	}

	defer func() {
		_ = rc.Close()
	}()

	pemText, err := io.ReadAll(io.LimitReader(rc, maxReleaseArchiveKey))
	if err != nil {
		return "", fmt.Errorf("%s: %w", releaseArchiveKey, err)
	}

	publicKey, _, err := evidence.ParsePublicKey(string(pemText))
	if err != nil {
		return "", fmt.Errorf("%s: %w", releaseArchiveKey, err)
	}

	return evidence.Fingerprint(publicKey)
}

func releasePublicKeyCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "public-key",
		Short: "Print the public key this server signs release manifests with",
		Long: "Give the key to the servers that import this server's release archives; " +
			"an administrator there trusts it with release trust-key.",
		Example: "  relic release public-key >release-key.pem",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := g.dial()
			if err != nil {
				return err
			}

			r, err := c.signingKeys.GetPublicKey(
				cmd.Context(),
				&relicv1.GetPublicKeyRequest{
					Name: releaseSigningKey,
				},
			)
			if err != nil {
				return fmt.Errorf("release public-key: %w", err)
			}

			_, _ = io.WriteString(cmd.OutOrStdout(), r.GetPublicKey())

			return nil
		},
	}
}

func releaseTrustKeyCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "trust-key <alias> <public-key-file>",
		Short: "Trust another server's release key, so its release archives import (administrators)",
		Long: "Adds the PEM public key to the server's trusted keys, which also verify evidence. " +
			"Get the key with release public-key on the exporting server, and compare its " +
			"fingerprint with the one release import reports.",
		Example: "  relic release trust-key build-server release-key.pem",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			pemText, err := os.ReadFile(args[1])
			if err != nil {
				return fmt.Errorf("release trust-key: %w", err)
			}

			c, err := g.dial()
			if err != nil {
				return err
			}

			r, err := c.evidence.AddTrustedKey(
				cmd.Context(),
				&relicv1.AddTrustedKeyRequest{
					Alias:     args[0],
					PublicKey: string(pemText),
				},
			)
			if err != nil {
				return fmt.Errorf("release trust-key: %w", err)
			}

			_, _ = fmt.Fprintf(
				cmd.OutOrStdout(),
				"trusted as %s (fingerprint %s)\n",
				r.GetKey().GetAlias(),
				r.GetKey().GetFingerprint(),
			)

			return nil
		},
	}
}

func releaseDeleteCmd(g *globals) *cobra.Command {
	var ref releaseRef

	cmd := &cobra.Command{
		Use:   "delete <name> <version>",
		Short: "Delete a release version and its stored files",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.dial()
			if err != nil {
				return err
			}

			if _, err := c.releases.DeleteRelease(
				cmd.Context(),
				&relicv1.DeleteReleaseRequest{
					Project: ref.project,
					Name:    args[0],
					Version: args[1],
				},
			); err != nil {
				return fmt.Errorf("release delete: %w", err)
			}

			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s/%s deleted\n", args[0], args[1])

			return nil
		},
	}
	ref.register(cmd)

	return cmd
}
