package main

import (
	"bytes"
	"cmp"
	"context"
	"crypto/md5"  //nolint:gosec // build info records MD5 checksums; nothing is trusted on them
	"crypto/sha1" //nolint:gosec // build info records SHA-1 checksums; nothing is trusted on them
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/platform/localdisk"

	"relic/internal/platform/terminal"
)

func (g *globals) buildDir() (string, error) {
	if d := g.getenv("RELIC_BUILD_DIR"); d != "" {
		return d, nil
	}

	p, err := g.configPath()
	if err != nil {
		return "", err
	}

	return filepath.Join(filepath.Dir(p), "builds"), nil
}

func (g *globals) buildFile(name, number string) (string, error) {
	dir, err := g.buildDir()
	if err != nil {
		return "", err
	}

	if name == "" || number == "" {
		return "", errors.New("a build needs a name and a number")
	}

	return filepath.Join(dir, url.PathEscape(name), url.PathEscape(number)+".json"), nil
}

func (g *globals) loadBuild(name, number string) (*relicv1.PublishedBuildRun, error) {
	f, err := g.buildFile(name, number)
	if err != nil {
		return nil, err
	}

	run := &relicv1.PublishedBuildRun{}

	b, err := os.ReadFile(f) //nolint:gosec // the user's own build directory
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return &relicv1.PublishedBuildRun{
			Name:         name,
			Number:       number,
			Started:      timestamppb.Now(),
			AgentName:    "relic",
			AgentVersion: "cli",
		}, nil
	case err != nil:
		return nil, err
	}

	if err := protojson.Unmarshal(b, run); err != nil {
		return nil, fmt.Errorf("read %s: %w", f, err)
	}

	return run, nil
}

func (g *globals) saveBuild(run *relicv1.PublishedBuildRun) error {
	f, err := g.buildFile(run.GetName(), run.GetNumber())
	if err != nil {
		return err
	}

	b, err := protojson.MarshalOptions{
		Multiline: true,
	}.Marshal(run)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
		return err
	}

	return writeFileAtomic(f, bytes.NewReader(b), 0o600)
}

func (g *globals) lockBuild(name, number string) (unlock func(), err error) {
	f, err := g.buildFile(name, number)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
		return nil, err
	}

	return localdisk.Lock(f + ".lock")
}

func (g *globals) forgetBuild(name, number string) error {
	f, err := g.buildFile(name, number)
	if err != nil {
		return err
	}

	for _, p := range []string{
		f,
		f + ".lock",
	} {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}

	return nil
}

func (g *globals) updateBuild(name, number string, change func(*relicv1.PublishedBuildRun) error) error {
	unlock, err := g.lockBuild(name, number)
	if err != nil {
		return err
	}
	defer unlock()

	run, err := g.loadBuild(name, number)
	if err != nil {
		return err
	}

	if err := change(run); err != nil {
		return err
	}

	return g.saveBuild(run)
}

func module(run *relicv1.PublishedBuildRun, id, typ string) *relicv1.BuildModule {
	for _, m := range run.GetModules() {
		if m.GetId() == id {
			return m
		}
	}

	m := &relicv1.BuildModule{
		Id:   id,
		Type: typ,
	}
	run.Modules = append(run.Modules, m)

	return m
}

func fileChecksums(name string) (*relicv1.Checksums, error) {
	f, err := os.Open(name) //nolint:gosec // a file the user names
	if err != nil {
		return nil, err
	}

	defer func() {
		_ = f.Close()
	}()

	hs := []hash.Hash{
		sha1.New(), //nolint:gosec // see the import
		sha256.New(),
		md5.New(), //nolint:gosec // see the import
	}

	if _, err := io.Copy(io.MultiWriter(hs[0], hs[1], hs[2]), f); err != nil {
		return nil, err
	}

	return &relicv1.Checksums{
		Sha1:   hex.EncodeToString(hs[0].Sum(nil)),
		Sha256: hex.EncodeToString(hs[1].Sum(nil)),
		Md5:    hex.EncodeToString(hs[2].Sum(nil)),
	}, nil
}

func buildArgs(args []string) (name, number string) { return args[0], args[1] }

func buildCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "build",
		Short: "Record, publish, promote, scan and clean up build runs",
		Long: "A build run is assembled locally, command by command, then published:\n\n" +
			"  relic build collect-env app 42\n" +
			"  relic build add-git app 42\n" +
			"  relic artifact upload --build app/42 target/app.jar libs-local/org/app/1.0/app.jar\n" +
			"  relic build add-dependencies app 42 ~/.m2/repository/org/lib/1.0/lib-1.0.jar\n" +
			"  relic build publish app 42\n\n" +
			"Runs in progress live in $RELIC_BUILD_DIR (default: builds/ beside the config file).",
	}
	cmd.AddCommand(
		buildCollectEnvCmd(g),
		buildAddGitCmd(g),
		buildAddDependenciesCmd(g),
		buildAddImageCmd(g),
		buildAppendCmd(g),
		buildPublishCmd(g),
		buildCleanCmd(g),
		buildDiscardCmd(g),
		buildPromoteCmd(g),
		buildScanCmd(g),
	)

	return cmd
}

var secretEnv = []string{
	"*pass*",
	"*psw*",
	"*secret*",
	"*key*",
	"*token*",
	"*auth*",
	"*credential*",
}

func matchesAny(patterns []string, name string) bool {
	name = strings.ToLower(name)

	return slices.ContainsFunc(patterns, func(p string) bool {
		ok, _ := path.Match(strings.ToLower(p), name)

		return ok
	})
}

func checkPatterns(flag string, patterns []string) error {
	for _, p := range patterns {
		if _, err := path.Match(p, ""); err != nil {
			return fmt.Errorf("%s %q: %w", flag, p, err)
		}
	}

	return nil
}

func buildCollectEnvCmd(g *globals) *cobra.Command {
	var include, exclude []string

	cmd := &cobra.Command{
		Use:   "collect-env <name> <number>",
		Short: "Record the environment variables as properties of the run",
		Long: "Records each variable as the property env.<NAME>. Variables whose names look secret " +
			"(" + strings.Join(secretEnv, ", ") + ") are left out; --exclude adds patterns, --include " +
			"limits the variables to those matching.",
		Example: "  relic build collect-env app 42 --include 'CI_*' --include 'GIT_*'",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkPatterns("--include", include); err != nil {
				return err
			}

			if err := checkPatterns("--exclude", exclude); err != nil {
				return err
			}

			name, number := buildArgs(args)
			n := 0

			err := g.updateBuild(name, number, func(run *relicv1.PublishedBuildRun) error {
				if run.Properties == nil {
					run.Properties = map[string]string{}
				}

				for _, kv := range os.Environ() {
					k, v, _ := strings.Cut(kv, "=")
					if matchesAny(append(slices.Clone(secretEnv), exclude...), k) ||
						(len(include) > 0 && !matchesAny(include, k)) {
						continue
					}

					run.Properties["env."+k] = v
					n++
				}

				return nil
			})
			if err != nil {
				return err
			}

			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s/%s: %d variables recorded\n", name, number, n)

			return nil
		},
	}
	cmd.Flags().StringArrayVar(&include, "include", nil, "record only variables matching this pattern (repeatable)")
	cmd.Flags().StringArrayVar(&exclude, "exclude", nil, "also leave out variables matching this pattern (repeatable)")

	return cmd
}

func buildAddGitCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:     "add-git <name> <number> [<directory>]",
		Short:   "Record the Git source of the run: remote URL, revision and branch",
		Long:    "Reads the working tree (default: the current directory). Credentials in the remote URL are not recorded.",
		Example: "  relic build add-git app 42",
		Args:    cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, number := buildArgs(args)

			dir := "."
			if len(args) == 3 {
				dir = args[2]
			}

			ctx := cmd.Context()

			rev, err := git(ctx, "-C", dir, "rev-parse", "HEAD")
			if err != nil {
				return err
			}

			vcs := &relicv1.VcsSource{
				Revision: strings.TrimSpace(string(rev)),
			}

			if b, err := git(ctx, "-C", dir, "rev-parse", "--abbrev-ref", "HEAD"); err == nil &&
				strings.TrimSpace(string(b)) != "HEAD" {
				vcs.Branch = strings.TrimSpace(string(b))
			}

			if u, err := git(ctx, "-C", dir, "remote", "get-url", "origin"); err == nil {
				vcs.Url = withoutCredentials(strings.TrimSpace(string(u)))
			}

			err = g.updateBuild(name, number, func(run *relicv1.PublishedBuildRun) error {
				run.Vcs = slices.DeleteFunc(run.Vcs, func(v *relicv1.VcsSource) bool {
					return v.GetUrl() == vcs.GetUrl()
				})
				run.Vcs = append(run.Vcs, vcs)

				return nil
			})
			if err != nil {
				return err
			}

			_, _ = fmt.Fprintf(
				cmd.OutOrStdout(),
				"%s/%s: %s %s %s\n",
				name,
				number,
				vcs.GetUrl(),
				vcs.GetBranch(),
				vcs.GetRevision(),
			)

			return nil
		},
	}
}

func withoutCredentials(s string) string {
	u, err := url.Parse(s)
	if err == nil && u.User != nil {
		u.User = nil

		return u.String()
	}

	scheme, rest, ok := strings.Cut(s, "://")
	at := strings.LastIndex(rest, "@")

	if !ok || at < 0 {
		return s
	}

	return scheme + "://" + rest[at+1:]
}

func buildAddDependenciesCmd(g *globals) *cobra.Command {
	var (
		moduleID, moduleType string
		fromRelic            bool
		scopes               []string
	)

	cmd := &cobra.Command{
		Use:   "add-dependencies <name> <number> <file>...",
		Short: "Record files the run used as dependencies",
		Long: "Records local files, or with --from-relic files stored in Relic (<repository>/<path>, their " +
			"checksums read from the server), as dependencies of a module (default: the build's name).",
		Example: "  relic build add-dependencies app 42 vendor/*.jar --scope compile\n" +
			"  relic build add-dependencies app 42 --from-relic libs-release/org/lib/1.0/lib-1.0.jar",
		Args: cobra.MinimumNArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, number := buildArgs(args)

			var deps []*relicv1.BuildDependency

			var c *client

			if fromRelic {
				var err error
				if c, err = g.dial(); err != nil {
					return err
				}
			}

			for _, f := range args[2:] {
				var (
					sums *relicv1.Checksums
					id   string
				)

				if fromRelic {
					repo, p, err := splitTarget(f)
					if err != nil {
						return err
					}

					r, err := c.artifacts.StatItem(
						cmd.Context(),
						&relicv1.StatItemRequest{
							Repository: repo,
							Path:       p,
						},
					)
					if err != nil {
						return fmt.Errorf("add-dependencies %s: %w", f, err)
					}

					sums, id = r.GetItem().GetChecksums(), path.Base(p)
				} else {
					var err error
					if sums, err = fileChecksums(f); err != nil {
						return err
					}

					id = filepath.Base(f)
				}

				deps = append(deps, &relicv1.BuildDependency{
					Id:     id,
					Sha1:   sums.GetSha1(),
					Sha256: sums.GetSha256(),
					Md5:    sums.GetMd5(),
					Scopes: scopes,
				})
			}

			err := g.updateBuild(name, number, func(run *relicv1.PublishedBuildRun) error {
				m := module(run, cmp.Or(moduleID, name), moduleType)
				m.Dependencies = append(m.Dependencies, deps...)

				return nil
			})
			if err != nil {
				return err
			}

			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s/%s: %d dependencies recorded\n", name, number, len(deps))

			return nil
		},
	}
	cmd.Flags().StringVar(&moduleID, "module", "", "module the dependencies belong to (default: the build's name)")
	cmd.Flags().StringVar(&moduleType, "type", "generic", "type of a new module (maven, npm, go, python, …)")
	cmd.Flags().BoolVar(&fromRelic, "from-relic", false, "the files are <repository>/<path> in Relic")
	cmd.Flags().StringSliceVar(&scopes, "scope", nil, "scopes of the dependencies (compile, test, …)")

	return cmd
}

// buildRunFlag is a --build value, <name>/<number>, checked when the flag
// is parsed.
type buildRunFlag struct {
	value string
}

func (f *buildRunFlag) String() string { return f.value }

func (f *buildRunFlag) Type() string { return "name/number" }

func (f *buildRunFlag) Set(s string) error {
	if _, _, err := parseBuildRef(s); err != nil {
		return fmt.Errorf("%q is not <name>/<number>", s)
	}

	f.value = s

	return nil
}

// parseBuildRef reads <name>/<number>; the name may itself contain '/'.
func parseBuildRef(ref string) (name, number string, err error) {
	i := strings.LastIndex(ref, "/")
	if i <= 0 || i == len(ref)-1 {
		return "", "", fmt.Errorf("--build %q is not <name>/<number>", ref)
	}

	return ref[:i], ref[i+1:], nil
}

// buildProperties are the build.name, build.number and build.timestamp
// properties of a file uploaded for a build run; the run's file is saved so
// its start is fixed.
func (g *globals) buildProperties(ref string) (url.Values, error) {
	name, number, err := parseBuildRef(ref)
	if err != nil {
		return nil, err
	}

	var started time.Time

	err = g.updateBuild(name, number, func(run *relicv1.PublishedBuildRun) error {
		started = run.GetStarted().AsTime()

		return nil
	})
	if err != nil {
		return nil, err
	}

	return url.Values{
		"build.name": {
			name,
		},
		"build.number": {
			number,
		},
		"build.timestamp": {
			strconv.FormatInt(started.UnixMilli(), 10),
		},
	}, nil
}

func (g *globals) addArtifact(build, moduleID string, it *relicv1.Item) error {
	name, number, err := parseBuildRef(build)
	if err != nil {
		return err
	}

	return g.updateBuild(name, number, func(run *relicv1.PublishedBuildRun) error {
		m := module(run, cmp.Or(moduleID, name), "generic")
		m.Artifacts = append(m.Artifacts, &relicv1.BuildArtifact{
			Name:   it.GetName(),
			Path:   it.GetRepository() + "/" + it.GetPath(),
			Sha1:   it.GetChecksums().GetSha1(),
			Sha256: it.GetChecksums().GetSha256(),
			Md5:    it.GetChecksums().GetMd5(),
		})

		return nil
	})
}

func imageRef(s string) (repo, image, tag string, err error) {
	repo, rest, _ := strings.Cut(s, "/")

	i := strings.LastIndex(rest, ":")
	if repo == "" || i <= 0 || strings.Contains(rest[i:], "/") {
		return "", "", "", fmt.Errorf("%q is not <repository>/<image>:<tag>", s)
	}

	return repo, rest[:i], rest[i+1:], nil
}

func buildAddImageCmd(g *globals) *cobra.Command {
	var moduleID string

	cmd := &cobra.Command{
		Use:   "add-image <name> <number> <repository>/<image>:<tag>",
		Short: "Record a container image in Relic as artifacts of the run",
		Long: "Records the image's manifest and layers, read from Relic, as a docker module " +
			"(default id: <image>:<tag>), so promotion and scanning of the run include the image.",
		Example: "  relic build add-image app 42 docker-local/team/app:1.4.0",
		Args:    cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, number := buildArgs(args)

			repo, image, tag, err := imageRef(args[2])
			if err != nil {
				return err
			}

			c, err := g.dial()
			if err != nil {
				return err
			}

			folder := image + "/" + tag

			items, err := c.files(cmd.Context(), repo, folder)
			if err != nil {
				return fmt.Errorf("add-image %s: %w", args[2], err)
			}

			if !slices.ContainsFunc(items, func(it *relicv1.Item) bool {
				return it.GetName() == "manifest.json"
			}) {
				return fmt.Errorf("add-image %s: no image manifest in %s/%s", args[2], repo, folder)
			}

			var arts []*relicv1.BuildArtifact
			for _, it := range items {
				arts = append(arts, &relicv1.BuildArtifact{
					Name:   it.GetName(),
					Path:   repo + "/" + it.GetPath(),
					Type:   "docker",
					Sha1:   it.GetChecksums().GetSha1(),
					Sha256: it.GetChecksums().GetSha256(),
					Md5:    it.GetChecksums().GetMd5(),
				})
			}

			err = g.updateBuild(name, number, func(run *relicv1.PublishedBuildRun) error {
				m := module(run, cmp.Or(moduleID, image+":"+tag), "docker")
				m.Artifacts = append(m.Artifacts, arts...)

				return nil
			})
			if err != nil {
				return err
			}

			_, _ = fmt.Fprintf(
				cmd.OutOrStdout(),
				"%s/%s: %s with %d files recorded\n",
				name,
				number,
				args[2],
				len(arts),
			)

			return nil
		},
	}
	cmd.Flags().StringVar(&moduleID, "module", "", "module id (default: <image>:<tag>)")

	return cmd
}

func buildAppendCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:     "append <name> <number> <other-name> <other-number>",
		Short:   "Record another published run as a module of this one",
		Long:    "Adds a build module <other-name>/<other-number> holding the other run's artifacts, so one run can gather the runs of a pipeline.",
		Example: "  relic build append release 7 backend 118",
		Args:    cobra.ExactArgs(4),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, number := buildArgs(args)

			c, err := g.dial()
			if err != nil {
				return err
			}

			other, err := c.builds.GetBuildRun(
				cmd.Context(),
				&relicv1.GetBuildRunRequest{
					Name:   args[2],
					Number: args[3],
				},
			)
			if err != nil {
				return fmt.Errorf("append %s/%s: %w", args[2], args[3], err)
			}

			err = g.updateBuild(name, number, func(run *relicv1.PublishedBuildRun) error {
				m := module(run, args[2]+"/"+args[3], "build")
				m.Artifacts = nil

				for _, om := range other.GetModules() {
					m.Artifacts = append(m.Artifacts, om.GetArtifacts()...)
				}

				return nil
			})
			if err != nil {
				return err
			}

			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s/%s: %s/%s appended\n", name, number, args[2], args[3])

			return nil
		},
	}
}

func buildPublishCmd(g *globals) *cobra.Command {
	var (
		project string
		dryRun  bool
		keep    bool
		output  string
	)

	cmd := &cobra.Command{
		Use:     "publish <name> <number>",
		Short:   "Publish the run assembled so far",
		Long:    "Publishes the run and forgets it locally (unless --keep). --dry-run prints it without publishing.",
		Example: "  relic build publish app 42\n  relic build publish app 42 --dry-run -o yaml",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, number := buildArgs(args)

			if !dryRun {
				unlock, err := g.lockBuild(name, number)
				if err != nil {
					return err
				}
				defer unlock()
			}

			run, err := g.loadBuild(name, number)
			if err != nil {
				return err
			}

			if !dryRun {
				c, err := g.dial()
				if err != nil {
					return err
				}

				if _, err := c.builds.PublishBuildRun(
					cmd.Context(),
					&relicv1.PublishBuildRunRequest{
						Project: project,
						Run:     run,
					},
				); err != nil {
					return fmt.Errorf("publish %s/%s: %w", name, number, err)
				}

				if !keep {
					if err := g.forgetBuild(name, number); err != nil {
						return err
					}
				}
			}

			return printMessage(cmd.OutOrStdout(), output, run, func(w io.Writer) error {
				verb := "published"
				if dryRun {
					verb = "would publish"
				}

				arts, deps := 0, 0
				for _, m := range run.GetModules() {
					arts += len(m.GetArtifacts())
					deps += len(m.GetDependencies())
				}

				_, err := fmt.Fprintf(
					w,
					"%s/%s %s: %d modules, %d artifacts, %d dependencies\n",
					name,
					number,
					verb,
					len(run.GetModules()),
					arts,
					deps,
				)

				return err
			})
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "project the build belongs to")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the run without publishing it")
	cmd.Flags().BoolVar(&keep, "keep", false, "keep the run locally after publishing")
	outputFlag(cmd, &output)

	return cmd
}

func buildCleanCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "clean <name> <number>",
		Short: "Forget a run assembled locally without publishing it",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, number := buildArgs(args)

			unlock, err := g.lockBuild(name, number)
			if err != nil {
				return err
			}
			defer unlock()

			if err := g.forgetBuild(name, number); err != nil {
				return err
			}

			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s/%s cleaned\n", args[0], args[1])

			return nil
		},
	}
}

func buildDiscardCmd(g *globals) *cobra.Command {
	var (
		project         string
		maxRuns, maxAge int32
		keep            []string
		deleteArtifacts bool
	)

	cmd := &cobra.Command{
		Use:     "discard <name>",
		Short:   "Delete old runs of a build: keep the newest, or those younger than an age",
		Example: "  relic build discard app --max-runs 20 --keep 1.0.0\n  relic build discard app --max-days 90 --delete-artifacts",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if maxRuns < 0 || maxAge < 0 {
				return errors.New("discard: --max-runs and --max-days cannot be negative")
			}

			if maxRuns == 0 && maxAge == 0 {
				return errors.New("discard: give --max-runs, --max-days or both")
			}

			c, err := g.dial()
			if err != nil {
				return err
			}

			req := &relicv1.ApplyBuildRetentionRequest{
				Project:         project,
				Name:            args[0],
				MaxRuns:         maxRuns,
				Keep:            keep,
				DeleteArtifacts: deleteArtifacts,
			}
			if maxAge > 0 {
				req.MinStarted = timestamppb.New(time.Now().AddDate(0, 0, -int(maxAge)))
			}

			if _, err := c.builds.ApplyBuildRetention(cmd.Context(), req); err != nil {
				return fmt.Errorf("discard %s: %w", args[0], err)
			}

			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s: old runs discarded\n", args[0])

			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "project the build belongs to")
	cmd.Flags().Int32Var(&maxRuns, "max-runs", 0, "keep at most this many runs, newest first")
	cmd.Flags().Int32Var(&maxAge, "max-days", 0, "discard runs started more than this many days ago")
	cmd.Flags().StringSliceVar(&keep, "keep", nil, "run numbers never discarded")
	cmd.Flags().BoolVar(&deleteArtifacts, "delete-artifacts", false, "delete the discarded runs' artifacts too")

	return cmd
}

func buildPromoteCmd(g *globals) *cobra.Command {
	var (
		project, status, comment, source string
		copyFiles, deps, dryRun          bool
		props                            []string
		output                           string
	)

	cmd := &cobra.Command{
		Use:   "promote <name> <number> [<target-repository>]",
		Short: "Move or copy a run's artifacts to a repository, recording a status",
		Long: "Moves (or with --copy copies) the run's artifacts, and with --dependencies its dependencies, " +
			"to the target repository, and records --status and --comment on the run. Without a target " +
			"only the status is recorded.",
		Example: "  relic build promote app 42 libs-release --status released --props release=1.0\n" +
			"  relic build promote app 42 libs-staging --copy --dry-run",
		Args:              cobra.RangeArgs(2, 3),
		ValidArgsFunction: completeRepositoriesAt(g, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, number := buildArgs(args)

			target := ""
			if len(args) == 3 {
				target = args[2]
			}

			properties, err := parseProperties(props)
			if err != nil {
				return err
			}

			c, err := g.dial()
			if err != nil {
				return err
			}

			r, err := c.builds.PromoteBuildRun(
				cmd.Context(),
				&relicv1.PromoteBuildRunRequest{
					Project:          project,
					Name:             name,
					Number:           number,
					Status:           status,
					Comment:          comment,
					SourceRepository: source,
					TargetRepository: target,
					Dependencies:     deps,
					Copy:             copyFiles,
					DryRun:           dryRun,
					Properties:       properties,
				},
			)
			if err != nil {
				return fmt.Errorf("promote %s/%s: %w", name, number, err)
			}

			failed := slices.ContainsFunc(r.GetMessages(), func(m *relicv1.PromotionMessage) bool {
				return m.GetLevel() == "error"
			})

			if err := printMessage(cmd.OutOrStdout(), output, r, func(w io.Writer) error {
				if len(r.GetMessages()) > 0 {
					table := terminal.Table{
						Headers: []string{
							"LEVEL",
							"MESSAGE",
						},
					}

					for _, m := range r.GetMessages() {
						table.Rows = append(table.Rows, []string{
							m.GetLevel(),
							tableCell(m.GetText()),
						})
					}

					if err := table.Print(w); err != nil {
						return err
					}
				}

				switch {
				case dryRun:
					_, _ = fmt.Fprintf(w, "%s/%s: dry run, nothing changed\n", name, number)
				case !failed:
					_, _ = fmt.Fprintf(w, "%s/%s promoted\n", name, number)
				}

				return nil
			}); err != nil {
				return err
			}

			if failed {
				return exitCode1
			}

			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "project the build belongs to")
	cmd.Flags().StringVar(&status, "status", "", "status recorded on the run (released, staged, …)")
	cmd.Flags().StringVar(&comment, "comment", "", "comment recorded with the status")
	cmd.Flags().StringVar(&source, "source-repository", "", "promote only the files in this repository")
	cmd.Flags().BoolVar(&copyFiles, "copy", false, "copy the files instead of moving them")
	cmd.Flags().BoolVar(&deps, "dependencies", false, "promote the run's dependencies too")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would happen without changing anything")
	cmd.Flags().
		StringArrayVar(&props, "props", nil, "property set on the promoted files, key=value[,value] (repeatable)")
	outputFlag(cmd, &output)

	return cmd
}

func completeRepositoriesAt(g *globals, i int) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) != i || strings.Contains(toComplete, "/") {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		out, _ := completeTargets(g)(cmd, args, toComplete)
		for j, s := range out {
			out[j] = strings.TrimSuffix(s, "/")
		}

		return out, cobra.ShellCompDirectiveNoFileComp
	}
}

func buildScanCmd(g *globals) *cobra.Command {
	var (
		project string
		wait    time.Duration
		fail    bool
		output  string
	)

	cmd := &cobra.Command{
		Use:   "scan <name> <number>",
		Short: "Scan a published run's dependencies and artifacts and report policy violations",
		Long: "Queues a scan of the run, waits for it (--wait) and prints the findings and the policy rules " +
			"they violate under the watches covering the build. Exits with status 1 when a violated rule " +
			"fails builds, unless --fail=false.",
		Example: "  relic build scan app 42\n  relic build scan app 42 -o json --fail=false",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, number := buildArgs(args)

			c, err := g.dial()
			if err != nil {
				return err
			}

			if _, err := c.scans.ScanBuildRun(
				cmd.Context(),
				&relicv1.ScanBuildRunRequest{
					Project: project,
					Name:    name,
					Number:  number,
				},
			); err != nil {
				return fmt.Errorf("scan %s/%s: %w", name, number, err)
			}

			res, err := waitBuildScan(cmd.Context(), c, project, name, number, wait)
			if err != nil {
				return err
			}

			if err := printMessage(cmd.OutOrStdout(), output, res, func(w io.Writer) error {
				printScan(w, name+"/"+number, res.GetScan(), res.GetViolations())

				return nil
			}); err != nil {
				return err
			}

			if fail && res.GetFailBuild() {
				return exitCode1
			}

			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "project the build belongs to")
	cmd.Flags().DurationVar(&wait, "wait", 10*time.Minute, "how long to wait for the scan")
	cmd.Flags().BoolVar(&fail, "fail", true, "exit with status 1 when a violated rule fails builds")
	outputFlag(cmd, &output)

	return cmd
}

func waitBuildScan(
	ctx context.Context,
	c *client,
	project, name, number string,
	wait time.Duration,
) (*relicv1.GetBuildRunScanResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()

	delay := 250 * time.Millisecond

	for {
		r, err := c.scans.GetBuildRunScan(
			ctx,
			&relicv1.GetBuildRunScanRequest{
				Project: project,
				Name:    name,
				Number:  number,
			},
		)
		if err != nil {
			return nil, fmt.Errorf("scan %s/%s: %w", name, number, err)
		}

		if r.GetScan().GetStatus() != relicv1.ScanStatus_SCAN_STATUS_QUEUED {
			if r.GetScan().GetStatus() == relicv1.ScanStatus_SCAN_STATUS_FAILED {
				return nil, fmt.Errorf("scan %s/%s failed: %s", name, number, r.GetScan().GetReason())
			}

			return r, nil
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("scan %s/%s: still queued after %s", name, number, wait)
		case <-time.After(delay):
			delay = min(2*delay, 5*time.Second)
		}
	}
}

func imageCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "image",
		Short: "Manage container images stored in Relic",
	}
	cmd.AddCommand(imagePromoteCmd(g))

	return cmd
}

func imagePromoteCmd(g *globals) *cobra.Command {
	var copyImage, allTags bool

	cmd := &cobra.Command{
		Use:   "promote <repository>/<image>:<tag> <target-repository>[/<image>[:<tag>]]",
		Short: "Move or copy an image tag to another repository, optionally renamed",
		Long: "Moves (or with --copy copies) the tag, or with --all-tags every tag of the image, to the " +
			"target repository; the target may rename the image and the tag. Moving needs delete " +
			"permission on the source.",
		Example: "  relic image promote docker-dev/team/app:1.4.0 docker-prod --copy\n" +
			"  relic image promote docker-dev/team/app:1.4.0 docker-prod/release/app:stable\n" +
			"  relic image promote docker-dev/team/app:any docker-archive --all-tags",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, image, tag, err := imageRef(args[0])
			if err != nil {
				return err
			}

			if allTags {
				tag = ""
			}

			targetRepo, target, _ := strings.Cut(args[1], "/")
			targetImage, targetTag := target, ""

			if i := strings.LastIndex(target, ":"); i > 0 {
				targetImage, targetTag = target[:i], target[i+1:]
			}

			c, err := g.dial()
			if err != nil {
				return err
			}

			if _, err := c.packages.PromoteContainerImage(
				cmd.Context(),
				&relicv1.PromoteContainerImageRequest{
					Repository:       repo,
					Image:            image,
					Tag:              tag,
					TargetRepository: targetRepo,
					TargetImage:      targetImage,
					TargetTag:        targetTag,
					Copy:             copyImage,
				},
			); err != nil {
				return fmt.Errorf("image promote %s: %w", args[0], err)
			}

			verb := "moved"
			if copyImage {
				verb = "copied"
			}

			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s %s to %s\n", args[0], verb, args[1])

			return nil
		},
	}
	cmd.Flags().BoolVar(&copyImage, "copy", false, "copy the image instead of moving it")
	cmd.Flags().BoolVar(&allTags, "all-tags", false, "promote every tag of the image")

	return cmd
}
