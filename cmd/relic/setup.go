package main

import (
	"bytes"
	"cmp"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/spf13/cobra"

	relicv1 "relic/api/gen/relic/v1"
)

type setupEnv struct {
	repo     string
	format   relicv1.Format
	url      string
	registry string
	files    string
	host     string
	user     string
	token    string
	// contextToken reports whether token is the saved context's own, which only the CLI holds.
	contextToken         bool
	certificateAuthority string
	authorityFile        string
	home, root           string
	goos                 string
	getenv               func(string) string
	class                relicv1.RepositoryClass
	depth                int32
	signingKeys          []signingKey
	flags                map[string]string
}

type signingKey struct {
	name, publicKey string
}

const tokenPlaceholder = "<token>"

type setupStep struct {
	file      string
	content   string
	block     bool
	soleBlock bool
	mode      fs.FileMode
	// command runs with stdin, env added to its environment, and argumentFile
	// passed as a private @file of one argument per line.
	command      []string
	stdin        string
	env          []string
	argumentFile []string
	// undo removes what an earlier setup made, so it may find nothing to remove.
	undo  bool
	owned bool
}

type setupClient struct {
	formats []relicv1.Format
	// packageURL is an example of the package URLs the client's packages have.
	packageURL string
	short      string
	flags      map[string]string
	steps      func(e setupEnv) ([]setupStep, error)
	notes      func(e setupEnv) string
	trust      func(e setupEnv) string
	system     bool
}

func withUserinfo(u, user, token string) (string, error) {
	p, err := url.Parse(u)
	if err != nil {
		return "", err
	}

	p.User = url.UserPassword(user, token)

	if token == tokenPlaceholder {
		return strings.Replace(p.String(), url.UserPassword("", token).String()+"@", ":"+token+"@", 1), nil
	}

	return p.String(), nil
}

func plainFileName(name string) bool {
	return name != "." && name != ".." && !strings.ContainsAny(name, `/\`) && filepath.IsLocal(name)
}

func armoredKeyring(keys []signingKey) (string, error) {
	var entities openpgp.EntityList

	for _, k := range keys {
		ring, err := openpgp.ReadArmoredKeyRing(strings.NewReader(k.publicKey))
		if err != nil {
			return "", fmt.Errorf("the public key of %s: %w", k.name, err)
		}

		entities = append(entities, ring...)
	}

	var buf bytes.Buffer

	w, err := armor.Encode(&buf, openpgp.PublicKeyType, nil)
	if err != nil {
		return "", err
	}

	for _, e := range entities {
		if err := e.Serialize(w); err != nil {
			return "", err
		}
	}

	if err := w.Close(); err != nil {
		return "", err
	}

	return buf.String() + "\n", nil
}

func withoutScheme(u string) string {
	return strings.TrimPrefix(strings.TrimPrefix(u, "https:"), "http:")
}

func npmSteps(e setupEnv) ([]setupStep, error) {
	return []setupStep{
		{
			file:    ".npmrc",
			block:   true,
			mode:    0o600,
			content: "registry=" + e.url + "\n" + withoutScheme(e.url) + ":_authToken=" + e.token + "\n",
		},
	}, nil
}

var setupClients = map[string]setupClient{
	"npm": {
		packageURL: "pkg:npm/lodash@4.17.21",
		formats: []relicv1.Format{
			relicv1.Format_FORMAT_NPM,
		},
		short: "~/.npmrc: the registry and a token for it",
		steps: npmSteps,
		trust: nodeTrust,
	},
	"pnpm": {
		packageURL: "pkg:npm/lodash@4.17.21",
		formats: []relicv1.Format{
			relicv1.Format_FORMAT_NPM,
		},
		short: "~/.npmrc, which pnpm reads",
		steps: npmSteps,
		trust: nodeTrust,
	},
	"yarn": {
		packageURL: "pkg:npm/lodash@4.17.21",
		formats: []relicv1.Format{
			relicv1.Format_FORMAT_NPM,
		},
		short: "~/.yarnrc.yml (Yarn 2 and later) and ~/.npmrc (Yarn 1)",
		steps: func(e setupEnv) ([]setupStep, error) {
			steps, _ := npmSteps(e)
			steps[0].content += withoutScheme(e.url) + ":always-auth=true\n"

			return append(steps, setupStep{
				file:      ".yarnrc.yml",
				block:     true,
				soleBlock: true,
				mode:      0o600,
				content:   "npmRegistryServer: \"" + e.url + "\"\nnpmAlwaysAuth: true\nnpmAuthToken: \"" + e.token + "\"\n",
			}), nil
		},
		trust: nodeTrust,
	},
	"maven": {
		packageURL: "pkg:maven/org.apache.commons/commons-lang3@3.14.0",
		formats: []relicv1.Format{
			relicv1.Format_FORMAT_MAVEN,
			relicv1.Format_FORMAT_GRADLE,
		},
		short: "~/.m2/settings.xml: a mirror of everything through the repository, with credentials",
		steps: func(e setupEnv) ([]setupStep, error) {
			return []setupStep{
				{
					file: ".m2/settings.xml",
					mode: 0o600,
					content: "<settings>\n  <servers>\n    <server>\n      <id>" + e.repo + "</id>\n      <username>" +
						xmlEscape(
							e.user,
						) + "</username>\n      <password>" + xmlEscape(e.token) + "</password>\n    </server>\n" +
						"  </servers>\n  <mirrors>\n    <mirror>\n      <id>" + e.repo + "</id>\n      <mirrorOf>*</mirrorOf>\n" +
						"      <url>" + xmlEscape(e.url) + "</url>\n    </mirror>\n  </mirrors>\n</settings>\n",
				},
			}, nil
		},
		notes: func(e setupEnv) string {
			return "To deploy, point distributionManagement at " + e.url + " with the server id " + e.repo + "."
		},
		trust: javaTrust,
	},
	"gradle": {
		packageURL: "pkg:maven/org.apache.commons/commons-lang3@3.14.0",
		formats: []relicv1.Format{
			relicv1.Format_FORMAT_GRADLE,
			relicv1.Format_FORMAT_MAVEN,
		},
		short: "~/.gradle/gradle.properties: the URL and credentials as relic* properties",
		steps: func(e setupEnv) ([]setupStep, error) {
			return []setupStep{
				{
					file:    ".gradle/gradle.properties",
					block:   true,
					mode:    0o600,
					content: "relicUrl=" + e.url + "\nrelicUser=" + e.user + "\nrelicToken=" + e.token + "\n",
				},
			}, nil
		},
		notes: func(_ setupEnv) string {
			return "In settings.gradle(.kts):\n" +
				"  dependencyResolutionManagement { repositories { maven {\n" +
				"    url = uri(providers.gradleProperty(\"relicUrl\").get())\n" +
				"    credentials { username = providers.gradleProperty(\"relicUser\").get(); " +
				"password = providers.gradleProperty(\"relicToken\").get() }\n" +
				"  } } }"
		},
		trust: javaTrust,
	},
	"go": {
		packageURL: "pkg:golang/github.com/google/uuid@v1.6.0",
		formats: []relicv1.Format{
			relicv1.Format_FORMAT_GOMOD,
		},
		short: "GOPROXY (go env -w) and ~/.netrc credentials for the go command",
		flags: map[string]string{
			"private": "module path patterns published to Relic, not in the public checksum database (GONOSUMDB)",
		},
		steps: func(e setupEnv) ([]setupStep, error) {
			steps := []setupStep{
				netrcStep(e),
				{
					command: []string{
						"go",
						"env",
						"-w",
						"GOPROXY=" + e.url,
					},
				},
			}
			if p := e.flags["private"]; p != "" {
				steps = append(steps, setupStep{
					command: []string{
						"go",
						"env",
						"-w",
						"GONOSUMDB=" + p,
					},
				})
			}

			return steps, nil
		},
		notes: func(_ setupEnv) string {
			return "Checksum verification stays on for public modules; name private module paths with --private."
		},
		trust: goTrust,
	},
	"pip": {
		packageURL: "pkg:pypi/requests@2.32.3",
		formats: []relicv1.Format{
			relicv1.Format_FORMAT_PYPI,
		},
		short: "pip.conf (pip.ini on Windows), where pip reads it: the index URL, and ~/.netrc credentials",
		steps: pipSteps,
	},
	"pipenv": {
		packageURL: "pkg:pypi/requests@2.32.3",
		formats: []relicv1.Format{
			relicv1.Format_FORMAT_PYPI,
		},
		short: "~/.netrc credentials; the Pipfile source to add",
		steps: func(e setupEnv) ([]setupStep, error) {
			return []setupStep{
				netrcStep(e),
			}, nil
		},
		notes: func(e setupEnv) string {
			return "In the Pipfile:\n  [[source]]\n  url = \"" + e.url + "\"\n  verify_ssl = true\n  name = \"" + e.repo + "\""
		},
		trust: func(e setupEnv) string {
			return "export PIP_CERT=" + e.authorityFile + " REQUESTS_CA_BUNDLE=" + e.authorityFile
		},
	},
	"poetry": {
		packageURL: "pkg:pypi/requests@2.32.3",
		formats: []relicv1.Format{
			relicv1.Format_FORMAT_PYPI,
		},
		short: "poetry config: the repository and its credentials",
		steps: func(e setupEnv) ([]setupStep, error) {
			steps := []setupStep{
				{
					command: []string{
						"poetry",
						"config",
						"repositories." + poetryRepository(e.repo),
						strings.TrimSuffix(e.url, "/simple"),
					},
				},
				{
					command: []string{
						"poetry",
						"config",
						"http-basic." + poetryRepository(e.repo),
						e.user,
					},
					stdin: e.token + "\n",
				},
			}
			if e.authorityFile != "" {
				steps = append(steps, setupStep{
					command: []string{
						"poetry",
						"config",
						"certificates." + poetryRepository(e.repo) + ".cert",
						e.authorityFile,
					},
				})
			}

			return steps, nil
		},
		notes: func(e setupEnv) string {
			name := poetryRepository(e.repo)

			return "For dependencies: poetry source add --priority=primary " + name + " " + e.url + "\nPublish with: poetry publish -r " + name
		},
	},
	"twine": {
		packageURL: "pkg:pypi/requests@2.32.3",
		formats: []relicv1.Format{
			relicv1.Format_FORMAT_PYPI,
		},
		short: "~/.pypirc: the repository to upload to, with credentials",
		steps: func(e setupEnv) ([]setupStep, error) {
			return []setupStep{
				{
					file: ".pypirc",
					mode: 0o600,
					content: "[distutils]\nindex-servers =\n    " + e.repo + "\n\n[" + e.repo + "]\nrepository = " +
						strings.TrimSuffix(
							e.url,
							"/simple",
						) + "\nusername = " + e.user + "\npassword = " + e.token + "\n" + optionalLine(
						"ca_cert = ",
						e.authorityFile,
					),
				},
			}, nil
		},
		notes: func(e setupEnv) string {
			return "Upload with: twine upload -r " + e.repo + " dist/*"
		},
	},
	"docker": {
		packageURL: "pkg:docker/library/nginx@1.27.0",
		formats: []relicv1.Format{
			relicv1.Format_FORMAT_DOCKER,
			relicv1.Format_FORMAT_OCI,
		},
		short: "docker login to the registry",
		steps: loginSteps("docker"),
		notes: func(e setupEnv) string {
			return "Images are " + e.registry + "/<image>:<tag>."
		},
		trust: func(e setupEnv) string {
			return "Docker trusts the authority as /etc/docker/certs.d/" + hostOnlyWithPort(e.registry) +
				"/ca.crt (on Docker Desktop, ~/.docker/certs.d/" + hostOnlyWithPort(e.registry) + "/ca.crt): copy " +
				e.authorityFile + " there."
		},
	},
	"podman": {
		packageURL: "pkg:docker/library/nginx@1.27.0",
		formats: []relicv1.Format{
			relicv1.Format_FORMAT_DOCKER,
			relicv1.Format_FORMAT_OCI,
		},
		short: "podman login to the registry",
		steps: func(e setupEnv) ([]setupStep, error) {
			steps, _ := loginSteps("podman")(e)
			if e.certificateAuthority == "" {
				return steps, nil
			}

			return append([]setupStep{
				{
					file:    ".config/containers/certs.d/" + hostOnlyWithPort(e.registry) + "/ca.crt",
					mode:    0o644,
					content: e.certificateAuthority,
					owned:   true,
				},
			}, steps...), nil
		},
		notes: func(e setupEnv) string {
			return "Images are " + e.registry + "/<image>:<tag>."
		},
	},
	"nuget": {
		packageURL: "pkg:nuget/Newtonsoft.Json@13.0.3",
		formats: []relicv1.Format{
			relicv1.Format_FORMAT_NUGET,
		},
		short: "dotnet nuget add source, with credentials",
		steps: func(e setupEnv) ([]setupStep, error) {
			return []setupStep{
				{
					command: []string{
						"dotnet",
						"nuget",
						"remove",
						"source",
						e.repo,
					},
					undo: true,
				},
				{
					command: []string{
						"dotnet",
					},
					argumentFile: []string{
						"nuget",
						"add",
						"source",
						e.url,
						"--name",
						e.repo,
						"--username",
						e.user,
						"--password",
						e.token,
						"--store-password-in-clear-text",
					},
				},
			}, nil
		},
		trust: systemTrust,
	},
	"conan": {
		packageURL: "pkg:conan/zlib@1.3.1",
		formats: []relicv1.Format{
			relicv1.Format_FORMAT_CONAN,
		},
		short: "conan remote add and login",
		steps: func(e setupEnv) ([]setupStep, error) {
			return []setupStep{
				{
					command: []string{
						"conan",
						"remote",
						"add",
						"--force",
						e.repo,
						e.url,
					},
				},
				{
					command: []string{
						"conan",
						"remote",
						"login",
						e.repo,
						e.user,
					},
					env: []string{
						"CONAN_PASSWORD=" + e.token,
					},
				},
			}, nil
		},
		trust: func(e setupEnv) string {
			return "In Conan's global.conf: core.net.http:cacert_path=" + e.authorityFile
		},
	},
	"ruby": {
		packageURL: "pkg:gem/rails@7.1.3",
		formats: []relicv1.Format{
			relicv1.Format_FORMAT_RUBYGEMS,
		},
		short: "bundler: a mirror of rubygems.org through the repository, with credentials",
		steps: func(e setupEnv) ([]setupStep, error) {
			return []setupStep{
				{
					command: []string{
						"bundle",
						"config",
						"set",
						"--global",
						"mirror.https://rubygems.org",
						e.url,
					},
				},
				{
					command: []string{
						"ruby",
						"-rbundler",
						"-e",
						"Bundler.settings.set_global(ARGV[0], $stdin.read.chomp)",
						e.url,
					},
					stdin: e.user + ":" + e.token,
				},
			}, nil
		},
		notes: func(e setupEnv) string {
			u, err := url.Parse(e.url)
			if err != nil {
				return "For gem: gem sources --add " + e.url
			}

			return "For gem: gem sources --add " + u.Scheme + "://" + url.User(e.user).String() + ":<token>@" +
				u.Host + u.EscapedPath()
		},
		trust: func(e setupEnv) string {
			return "bundle config set --global ssl_ca_cert " + e.authorityFile + "\nFor gem, in ~/.gemrc: :ssl_ca_cert: " +
				e.authorityFile
		},
	},
	"terraform": {
		formats: []relicv1.Format{
			relicv1.Format_FORMAT_TERRAFORM,
		},
		short: "~/.terraformrc (terraform.rc on Windows): credentials for modules, and the repository as the providers' network mirror",
		steps: func(e setupEnv) ([]setupStep, error) {
			return []setupStep{
				{
					file:  terraformConfigFile(e),
					block: true,
					mode:  0o600,
					content: "credentials \"" + e.host + "\" {\n  token = \"" + e.token + "\"\n}\n" +
						"provider_installation {\n  network_mirror {\n    url = \"" + e.url + "\"\n  }\n}\n",
				},
			}, nil
		},
		notes: func(e setupEnv) string {
			return "Modules: source = \"" + e.host + "/" + e.repo + "__<namespace>/<name>/<provider>\"; " +
				"publish them with relic package publish terraform."
		},
		trust: goTrust,
	},
	"helm": {
		formats: []relicv1.Format{
			relicv1.Format_FORMAT_HELM,
			relicv1.Format_FORMAT_HELM_OCI,
		},
		short: "helm repo add (or helm registry login for OCI charts)",
		steps: func(e setupEnv) ([]setupStep, error) {
			var caFile []string
			if e.authorityFile != "" {
				caFile = []string{
					"--ca-file",
					e.authorityFile,
				}
			}

			if e.format == relicv1.Format_FORMAT_HELM_OCI {
				return []setupStep{
					{
						command: append([]string{
							"helm",
							"registry",
							"login",
							hostOnlyWithPort(e.registry),
							"--username",
							e.user,
							"--password-stdin",
						}, caFile...),
						stdin: e.token,
					},
				}, nil
			}

			return []setupStep{
				{
					command: append([]string{
						"helm",
						"repo",
						"add",
						"--force-update",
						e.repo,
						e.url,
						"--username",
						e.user,
						"--password-stdin",
					}, caFile...),
					stdin: e.token,
				},
			}, nil
		},
	},
	"cargo": {
		packageURL: "pkg:cargo/serde@1.0.203",
		formats: []relicv1.Format{
			relicv1.Format_FORMAT_CARGO,
		},
		short: "~/.cargo/config.toml (the sparse registry) and credentials.toml (its token)",
		steps: func(e setupEnv) ([]setupStep, error) {
			return []setupStep{
				{
					file:  ".cargo/config.toml",
					block: true,
					mode:  0o644,
					content: "[registries." + e.repo + "]\nindex = \"sparse+" + e.url + "\"\n" +
						"credential-provider = [\"cargo:token\"]\n",
				},
				{
					file:    ".cargo/credentials.toml",
					block:   true,
					mode:    0o600,
					content: "[registries." + e.repo + "]\ntoken = \"Bearer " + e.token + "\"\n",
				},
			}, nil
		},
		notes: func(e setupEnv) string {
			return "Use it with --registry " + e.repo + ", or replace crates.io:\n  [source.crates-io]\n  replace-with = \"" + e.repo + "\""
		},
		trust: func(e setupEnv) string {
			return "export CARGO_HTTP_CAINFO=" + e.authorityFile
		},
	},
	"apk": {
		packageURL: "pkg:apk/alpine/curl@8.9.1-r0",
		formats: []relicv1.Format{
			relicv1.Format_FORMAT_ALPINE,
		},
		short:  "/etc/apk/repositories and the repository's signing key (run as root, e.g. in an image build)",
		system: true,
		flags: map[string]string{
			"branch":              "the Alpine branch, as the repository lays it out (v3.21, edge)",
			"alpine-repositories": "the Alpine repositories of the branch, space-separated (default main)",
		},
		steps: func(e setupEnv) ([]setupStep, error) {
			branch := e.flags["branch"]
			if branch == "" {
				return nil, errors.New("apk: give the branch (--branch v3.21)")
			}

			var lines strings.Builder

			for _, name := range strings.Fields(cmp.Or(e.flags["alpine-repositories"], "main")) {
				if !plainFileName(name) || !plainFileName(branch) {
					return nil, fmt.Errorf("apk: %s/%s is not a branch and repository of the layout", branch, name)
				}

				repository, err := withUserinfo(strings.TrimSuffix(e.url, "/")+"/"+branch+"/"+name, e.user, e.token)
				if err != nil {
					return nil, fmt.Errorf("apk: %w", err)
				}

				lines.WriteString(repository + "\n")
			}

			steps := []setupStep{
				{
					file:    "etc/apk/repositories",
					block:   true,
					mode:    0o600,
					content: lines.String(),
				},
			}

			for _, key := range e.signingKeys {
				if !plainFileName(key.name) {
					return nil, fmt.Errorf("apk: the key pair name %q cannot name a key file", key.name)
				}

				steps = append(steps, setupStep{
					file:    "etc/apk/keys/" + key.name + ".rsa.pub",
					mode:    0o644,
					content: key.publicKey,
				})
			}

			return steps, nil
		},
		trust: func(e setupEnv) string {
			return "apk trusts the system's authorities: copy " + e.authorityFile +
				" to /usr/local/share/ca-certificates/ and run update-ca-certificates."
		},
	},
	"apt": {
		packageURL: "pkg:deb/debian/curl@7.88.1-10",
		formats: []relicv1.Format{
			relicv1.Format_FORMAT_DEBIAN,
		},
		short:  "an APT source, its credentials and the repository's signing key (run as root)",
		system: true,
		flags: map[string]string{
			"distribution": "the distribution (bookworm, noble)",
			"components":   "the components, space-separated (default main)",
		},
		steps: func(e setupEnv) ([]setupStep, error) {
			dist := e.flags["distribution"]
			if dist == "" {
				return nil, errors.New("apt: give the distribution (--distribution bookworm)")
			}

			components := cmp.Or(e.flags["components"], "main")
			signedBy := ""

			var steps []setupStep

			if len(e.signingKeys) > 0 {
				keyring, err := armoredKeyring(e.signingKeys)
				if err != nil {
					return nil, fmt.Errorf("apt: %w", err)
				}

				signedBy = "[signed-by=/etc/apt/keyrings/relic-" + e.repo + ".asc] "
				steps = append(steps, setupStep{
					file:    "etc/apt/keyrings/relic-" + e.repo + ".asc",
					mode:    0o644,
					content: keyring,
				})
			}

			if e.authorityFile != "" {
				steps = append(steps, setupStep{
					file:    "etc/apt/apt.conf.d/80relic-" + e.repo,
					mode:    0o644,
					content: "Acquire::https::" + hostOnly(e.host) + "::CaInfo \"" + e.authorityFile + "\";\n",
				})
			}

			return append(
				steps,
				setupStep{
					file:    "etc/apt/sources.list.d/relic-" + e.repo + ".list",
					mode:    0o644,
					content: "deb " + signedBy + e.url + " " + dist + " " + components + "\n",
				},
				setupStep{
					file:    "etc/apt/auth.conf.d/relic-" + e.repo + ".conf",
					mode:    0o600,
					content: "machine " + e.url + " login " + e.user + " password " + e.token + "\n",
				},
			), nil
		},
	},
	"dnf": {
		packageURL: "pkg:rpm/fedora/curl@8.6.0-1",
		formats: []relicv1.Format{
			relicv1.Format_FORMAT_RPM,
		},
		short:  "a dnf and yum repository file with its credentials, reading the repository's signing keys from Relic (run as root)",
		system: true,
		flags: map[string]string{
			"folder": "the folder whose repodata to use, in a repository with a metadataDepth (el9, f44)",
		},
		steps: func(e setupEnv) ([]setupStep, error) {
			baseURL, err := dnfBaseURL(e)
			if err != nil {
				return nil, err
			}

			signature := "repo_gpgcheck=0\n"
			if len(e.signingKeys) > 0 {
				signature = "repo_gpgcheck=1\ngpgkey=" + strings.TrimSuffix(baseURL, "/") + "/repodata/repomd.xml.key\n"
			}

			return []setupStep{
				{
					file: "etc/yum.repos.d/relic-" + e.repo + ".repo",
					mode: 0o600,
					content: "[relic-" + e.repo + "]\nname=Relic " + e.repo + "\nbaseurl=" + baseURL + "\nusername=" + e.user +
						"\npassword=" + e.token + "\nenabled=1\ngpgcheck=0\n" + signature + optionalLine(
						"sslcacert=",
						e.authorityFile,
					),
				},
			}, nil
		},
		notes: func(e setupEnv) string {
			if e.class != relicv1.RepositoryClass_REPOSITORY_CLASS_REMOTE {
				return ""
			}

			return "The upstream signs this remote's metadata and packages, not Relic: add the upstream's gpgkey= " +
				"and set repo_gpgcheck=1 and gpgcheck=1 to check them."
		},
	},
	"nix": {
		formats: []relicv1.Format{
			relicv1.Format_FORMAT_NIX,
		},
		short: "~/.config/nix/nix.conf (the binary cache as a substituter) and ~/.netrc credentials",
		steps: func(e setupEnv) ([]setupStep, error) {
			return []setupStep{
				{
					file:  ".config/nix/nix.conf",
					block: true,
					mode:  0o644,
					content: "extra-substituters = " + e.url + "\nnetrc-file = " +
						filepath.Join(e.home, ".netrc") + "\n",
				},
				netrcStep(e),
			}, nil
		},
		notes: func(_ setupEnv) string {
			return "Add the cache's signing key to trusted-public-keys, or set require-sigs = false for unsigned caches."
		},
		trust: func(e setupEnv) string {
			return "In nix.conf (the daemon's /etc/nix/nix.conf on a multi-user install): ssl-cert-file = " +
				e.authorityFile
		},
	},
	"huggingface": {
		formats: []relicv1.Format{
			relicv1.Format_FORMAT_HUGGINGFACE,
		},
		short: "the token, where huggingface_hub reads it (HF_TOKEN_PATH, HF_HOME, ~/.cache/huggingface); HF_ENDPOINT to export",
		steps: func(e setupEnv) ([]setupStep, error) {
			return []setupStep{
				{
					file:    huggingFaceTokenFile(e),
					mode:    0o600,
					content: e.token,
				},
			}, nil
		},
		notes: func(e setupEnv) string {
			return "export HF_ENDPOINT=" + e.url + "\nor relic package huggingface upload|download, which need neither."
		},
		trust: func(e setupEnv) string {
			return "export SSL_CERT_FILE=" + e.authorityFile + " REQUESTS_CA_BUNDLE=" + e.authorityFile
		},
	},
}

func nodeTrust(e setupEnv) string {
	return "export NODE_EXTRA_CA_CERTS=" + e.authorityFile
}

func javaTrust(e setupEnv) string {
	return "Java trusts the authority once it is in the JDK's trust store: keytool -importcert -cacerts -alias relic -file " +
		e.authorityFile
}

func goTrust(e setupEnv) string {
	return "export SSL_CERT_FILE=" + e.authorityFile + " (Linux); on macOS and Windows, add it to the system trust store."
}

func systemTrust(e setupEnv) string {
	return "Add " + e.authorityFile + " to the system trust store."
}

func optionalLine(prefix, value string) string {
	if value == "" {
		return ""
	}

	return prefix + value + "\n"
}

func authorityStep(e setupEnv, client setupClient) setupStep {
	return setupStep{
		file:    authorityFileName(e, client),
		mode:    0o644,
		content: e.certificateAuthority,
		owned:   true,
	}
}

func authorityFileName(e setupEnv, client setupClient) string {
	name := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' {
			return r
		}

		return '_'
	}, e.host)

	if client.system {
		return "etc/relic/certificate-authorities/" + name + ".crt"
	}

	return ".config/relic/certificate-authorities/" + name + ".crt"
}

func withAuthority(e setupEnv, client setupClient) setupEnv {
	switch {
	case e.certificateAuthority == "":
		e.authorityFile = ""
	case client.system:
		e.authorityFile = "/" + authorityFileName(e, client)
	default:
		e.authorityFile = stepPath(e, client, authorityFileName(e, client))
	}

	return e
}

func clientSteps(e setupEnv, client setupClient) ([]setupStep, error) {
	steps, err := client.steps(e)
	if err != nil || e.authorityFile == "" {
		return steps, err
	}

	return append([]setupStep{
		authorityStep(e, client),
	}, steps...), nil
}

func trustNote(e setupEnv, client setupClient) string {
	if e.authorityFile == "" || client.trust == nil {
		return ""
	}

	return "The server's certificate authority is in " + e.authorityFile + ".\n" + client.trust(e)
}

// homeEnvironment is e.getenv when e.home is the user's own home, and
// otherwise a getenv that finds no variable: they describe that home only.
func homeEnvironment(e setupEnv) func(string) string {
	if e.getenv == nil || e.home != userHome() {
		return func(string) string {
			return ""
		}
	}

	return e.getenv
}

// pipConfigFile is the user configuration file pip reads on e.goos:
// %APPDATA%\pip\pip.ini on Windows, $XDG_CONFIG_HOME/pip/pip.conf elsewhere
// but macOS.
func pipConfigFile(e setupEnv) string {
	getenv := homeEnvironment(e)

	switch e.goos {
	case "windows":
		if appData := getenv("APPDATA"); filepath.IsAbs(appData) {
			return filepath.Join(appData, "pip", "pip.ini")
		}

		return "AppData/Roaming/pip/pip.ini"
	case "darwin":
		if st, err := os.Stat(
			filepath.Join(e.home, "Library", "Application Support", "pip"),
		); err == nil &&
			st.IsDir() {
			return "Library/Application Support/pip/pip.conf"
		}
	default:
		if configHome := getenv("XDG_CONFIG_HOME"); filepath.IsAbs(configHome) {
			return filepath.Join(configHome, "pip", "pip.conf")
		}
	}

	return ".config/pip/pip.conf"
}

// terraformConfigFile is the CLI configuration file terraform reads on e.goos:
// TF_CLI_CONFIG_FILE, %APPDATA%\terraform.rc on Windows, ~/.terraformrc
// elsewhere.
func terraformConfigFile(e setupEnv) string {
	getenv := homeEnvironment(e)

	if file := getenv("TF_CLI_CONFIG_FILE"); filepath.IsAbs(file) {
		return file
	}

	if e.goos == "windows" {
		if appData := getenv("APPDATA"); filepath.IsAbs(appData) {
			return filepath.Join(appData, "terraform.rc")
		}

		return "AppData/Roaming/terraform.rc"
	}

	return ".terraformrc"
}

func poetryRepository(repo string) string {
	// Poetry reserves pypi for PyPI, whose credentials poetry publish sends to upload.pypi.org.
	if strings.EqualFold(repo, "pypi") {
		return "relic-" + repo
	}

	return repo
}

func pipSteps(e setupEnv) ([]setupStep, error) {
	return []setupStep{
		{
			file:    pipConfigFile(e),
			mode:    0o644,
			content: "[global]\nindex-url = " + e.url + "\n" + optionalLine("cert = ", e.authorityFile),
		},
		netrcStep(e),
	}, nil
}

func netrcStep(e setupEnv) setupStep {
	// The go command matches host:port; curl and Python's netrc match the host alone.
	credentials := " login " + e.user + " password " + e.token + "\n"

	content := "machine " + hostOnly(e.host) + credentials
	if e.host != hostOnly(e.host) {
		content = "machine " + e.host + credentials + content
	}

	return setupStep{
		file:    ".netrc",
		block:   true,
		mode:    0o600,
		content: content,
	}
}

func loginSteps(client string) func(e setupEnv) ([]setupStep, error) {
	return func(e setupEnv) ([]setupStep, error) {
		return []setupStep{
			{
				command: []string{
					client,
					"login",
					hostOnlyWithPort(e.registry),
					"--username",
					e.user,
					"--password-stdin",
				},
				stdin: e.token,
			},
		}, nil
	}
}

func hostOnly(host string) string {
	if h, _, ok := strings.Cut(host, ":"); ok {
		return h
	}

	return host
}

func hostOnlyWithPort(registry string) string {
	h, _, _ := strings.Cut(registry, "/")

	return h
}

func xmlEscape(s string) string {
	var b bytes.Buffer

	_ = xml.EscapeText(&b, []byte(s))

	return b.String()
}

func serverSetupCmd(g *globals) *cobra.Command {
	var (
		home, root    string
		yes, system   bool
		shell, listen string
	)

	cmd := &cobra.Command{
		Use: "setup",
		Annotations: map[string]string{
			progressAnnotation: "off",
		},
		Short: "Install relic on this machine: config, shell completion and the server as a service",
		Long: "Shows the client's config file, writes shell completion and loads it from ~/.bashrc (or ~/.zshrc, " +
			"or fish's completions directory), and runs the server (relic server start --worker on SQLite and the " +
			"filesystem) as a service: a systemd user service that starts at boot on Linux (with --system, as root, a " +
			"system service with its own user), or a launch agent that starts at login on macOS. It lists what it " +
			"will change and asks first (--yes does not); running it again changes nothing.",
		Example: "  relic server setup\n" +
			"  relic server setup --yes --listen 0.0.0.0:8090\n" +
			"  sudo relic server setup --system",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, err := newMachineSetup(cmd, g, home, root)
			if err != nil {
				return err
			}

			m.yes, m.system, m.listen = yes, system, listen
			m.listenSet = cmd.Flags().Changed("listen")

			if shell != "" {
				m.shell = shell
			}

			return m.runSetup(cmd.Context())
		},
	}
	cmd.Flags().StringVar(&home, "home", userHome(), "the home directory user files go under")
	cmd.Flags().StringVar(&root, "root", "/", "the root directory system files go under")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask before changing anything")
	cmd.Flags().BoolVar(&system, "system", false, "as root, on Linux: run the server as a systemd system service")
	cmd.Flags().StringVar(&shell, "shell", "", "the shell to complete for (default from $SHELL)")
	cmd.Flags().StringVar(&listen, "listen", "127.0.0.1:8090", "the address the service listens on")

	return cmd
}

func packageSetupCmd(g *globals) *cobra.Command {
	var (
		write, force, showToken, agent bool
		home, root, project            string
		expiresDays                    int32
		values                         = map[string]*string{}
	)

	names := slices.Sorted(func(yield func(string) bool) {
		for n := range setupClients {
			if !yield(n) {
				return
			}
		}
	})

	var list strings.Builder
	for _, n := range names {
		_, _ = fmt.Fprintf(&list, "  %-12s %s\n", n, setupClients[n].short)
	}

	cmd := &cobra.Command{
		Use:   "setup <client> <repository>",
		Short: "Configure a package manager client for a repository",
		Long: "Prints the configuration a client needs to use a repository with a token (shown as <token> " +
			"unless --show-token), or with --write writes it: user files under --home, system files under " +
			"--root, and runs the client's own login commands. Signed in with a context, the client gets a " +
			"token of its own (described as relic package setup <client> <repository>), so signing in again " +
			"or signing out leaves it working; --token or RELIC_TOKEN is given to the client as it is. " +
			"Files are written 0600 when they hold the " +
			"token; in files other tools share (.npmrc, .netrc, …) relic writes a marked block and keeps the rest. " +
			"Clients:\n\n" + list.String(),
		Example: "  relic package setup npm npm-virtual --write\n" +
			"  relic package setup maven maven-virtual\n" +
			"  relic package setup go go-virtual --private 'git.example.com/*' --write\n" +
			"  relic package setup npm npm-virtual --agent --project .\n" +
			"  relic package setup apt debian-remote --distribution bookworm --write   # as root, e.g. in a Dockerfile",
		Args: cobra.ExactArgs(2),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				return names, cobra.ShellCompDirectiveNoFileComp
			}

			return completeRepositoriesAt(g, 1)(cmd, args, toComplete)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			client, ok := setupClients[args[0]]
			if !ok {
				return fmt.Errorf("unknown client %q: use one of %s", args[0], strings.Join(names, ", "))
			}

			c, err := g.dial()
			if err != nil {
				return err
			}

			e, err := setupEnvFor(cmd, g, c, client, args[1])
			if err != nil {
				return err
			}

			e.home, e.root, e.goos, e.getenv = home, root, runtime.GOOS, g.getenv
			e.flags = map[string]string{}

			for k, v := range values {
				e.flags[k] = *v
			}

			write = write || agent

			switch {
			case !write && !showToken:
				e.token = tokenPlaceholder
			case e.contextToken:
				e.token, err = clientToken(cmd, c, args[0], args[1], expiresDays)
				if err != nil {
					return err
				}
			}

			e = withAuthority(e, client)

			steps, err := clientSteps(e, client)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()

			for _, s := range steps {
				if write {
					if err := applySetupStep(cmd, e, client, s, force); err != nil {
						return err
					}

					continue
				}

				printSetupStep(out, e, client, s)
			}

			if agent {
				if err := writeAgentFiles(out, project, e.url, args[0], args[1]); err != nil {
					return err
				}
			}

			if client.notes != nil && client.notes(e) != "" {
				_, _ = fmt.Fprintf(out, "\n%s\n", client.notes(e))
			}

			if note := trustNote(e, client); note != "" {
				_, _ = fmt.Fprintf(out, "\n%s\n", note)
			}

			return nil
		},
	}
	cmd.Flags().BoolVar(&write, "write", false, "write the files and run the commands instead of printing them")
	cmd.Flags().BoolVar(&agent, "agent", false, "also route coding agents through the repository: write the client's "+
		"files (--write), an AGENTS.md section and relic mcp in .mcp.json under --project")
	cmd.Flags().StringVar(&project, "project", ".", "the project directory --agent writes AGENTS.md and .mcp.json into")
	cmd.Flags().BoolVar(&force, "force", false, "replace files relic does not write a block into")
	cmd.Flags().BoolVar(&showToken, "show-token", false, "print the token instead of <token>")
	cmd.Flags().
		StringVar(&home, "home", userHome(), "the home directory user files go under, and the client commands run with")
	cmd.Flags().StringVar(&root, "root", "/", "the root directory system files go under")
	cmd.Flags().Int32Var(&expiresDays, "expires-days", 90, "lifetime in days of the token issued for the client; "+
		"0 never expires for administrators and is 365 for other users")

	for _, n := range names {
		for flag, help := range setupClients[n].flags {
			if _, ok := values[flag]; ok {
				continue
			}

			values[flag] = new(string)
			cmd.Flags().StringVar(values[flag], flag, "", help+" ("+n+")")
		}
	}

	return cmd
}

func userHome() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	return h
}

func setupEnvFor(cmd *cobra.Command, g *globals, c *client, client setupClient, repo string) (setupEnv, error) {
	ctx := cmd.Context()

	r, err := c.repositories.GetRepository(ctx, &relicv1.GetRepositoryRequest{
		Name: repo,
	})
	if err != nil {
		return setupEnv{}, fmt.Errorf("setup: %w", err)
	}

	format := r.GetRepository().GetFormat()
	if !slices.Contains(client.formats, format) {
		return setupEnv{}, fmt.Errorf("setup: %s is a %s repository", repo, formatName(format))
	}

	ep, err := c.repositories.GetClientEndpoints(ctx, &relicv1.GetClientEndpointsRequest{
		Name: repo,
	})
	if err != nil {
		return setupEnv{}, fmt.Errorf("setup: %w", err)
	}

	me, err := c.sessions.GetCurrentUser(ctx, &relicv1.GetCurrentUserRequest{})
	if err != nil {
		return setupEnv{}, fmt.Errorf("setup: %w", err)
	}

	target, err := g.target()
	if err != nil {
		return setupEnv{}, err
	}

	if target.token == "" {
		return setupEnv{}, fmt.Errorf(
			"setup: no token to give the client: sign in with relic auth login %s --user %s",
			target.url,
			me.GetPrincipal().GetUsername(),
		)
	}

	u, err := url.Parse(c.url)
	if err != nil {
		return setupEnv{}, err
	}

	e := setupEnv{
		repo:                 repo,
		format:               format,
		url:                  ep.GetPackageManager(),
		registry:             ep.GetContainerRegistry(),
		files:                ep.GetFiles(),
		host:                 u.Host,
		user:                 me.GetPrincipal().GetUsername(),
		token:                target.token,
		contextToken:         target.contextToken,
		class:                r.GetRepository().GetClass(),
		depth:                r.GetRepository().GetMetadataDepth(),
		certificateAuthority: target.certificateAuthority,
	}
	if e.url == "" {
		e.url = e.files
	}

	if slices.Contains(signedFormats, format) && e.class != relicv1.RepositoryClass_REPOSITORY_CLASS_REMOTE {
		key, err := c.repositories.GetRepositorySigningKey(
			ctx,
			&relicv1.GetRepositorySigningKeyRequest{
				Name: repo,
			},
		)
		if err != nil {
			return setupEnv{}, fmt.Errorf("setup: the signing key of %s: %w", repo, err)
		}

		e.signingKeys = []signingKey{
			{
				name:      key.GetKeyPair(),
				publicKey: key.GetPublicKey(),
			},
		}
		if key.GetSecondaryKeyPair() != "" {
			e.signingKeys = append(e.signingKeys, signingKey{
				name:      key.GetSecondaryKeyPair(),
				publicKey: key.GetSecondaryPublicKey(),
			})
		}
	}

	return e, nil
}

func dnfBaseURL(e setupEnv) (string, error) {
	folder := strings.Trim(e.flags["folder"], "/")
	if e.depth == 0 {
		if folder != "" {
			return "", fmt.Errorf("dnf: %s serves its repodata at its root, so it takes no --folder", e.repo)
		}

		return e.url, nil
	}

	segments := strings.Split(folder, "/")
	if folder == "" || len(segments) != int(e.depth) || slices.ContainsFunc(segments, func(segment string) bool {
		return !plainFileName(segment)
	}) {
		return "", fmt.Errorf(
			"dnf: %s has repodata %d folder(s) down: give the folder (--folder el9)",
			e.repo,
			e.depth,
		)
	}

	return strings.TrimSuffix(e.url, "/") + "/" + folder, nil
}

// clientToken issues the token a client gets in place of the context's own, which signing in again and
// signing out revoke.
func clientToken(cmd *cobra.Command, c *client, clientName, repo string, expiresDays int32) (string, error) {
	resp, err := c.security.CreateToken(cmd.Context(), &relicv1.CreateTokenRequest{
		Description:      "relic package setup " + clientName + " " + repo,
		ExpiresInSeconds: int64(expiresDays) * secondsPerDay,
	})
	if err != nil {
		return "", fmt.Errorf("setup: issue a token for %s: %w", clientName, err)
	}

	return resp.GetAccessToken(), nil
}

var signedFormats = []relicv1.Format{
	relicv1.Format_FORMAT_ALPINE,
	relicv1.Format_FORMAT_DEBIAN,
	relicv1.Format_FORMAT_RPM,
}

func formatName(f relicv1.Format) string {
	return strings.TrimPrefix(strings.ToLower(f.String()), "format_")
}

func writeArgumentFile(arguments []string) (string, error) {
	f, err := os.CreateTemp("", "relic-setup-*.rsp")
	if err != nil {
		return "", err
	}

	var lines strings.Builder

	for _, argument := range arguments {
		if strings.ContainsAny(argument, "\r\n") || strings.HasPrefix(argument, "#") {
			_ = f.Close()
			_ = os.Remove(f.Name())

			return "", errors.New("an argument with a line break or a leading # cannot go in an argument file")
		}

		lines.WriteString(argument + "\n")
	}

	_, err = f.WriteString(lines.String())
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}

	if err != nil {
		_ = os.Remove(f.Name())

		return "", err
	}

	return f.Name(), nil
}

// huggingFaceTokenFile is the file huggingface_hub reads its token from:
// HF_TOKEN_PATH, $HF_HOME/token, $XDG_CACHE_HOME/huggingface/token or
// ~/.cache/huggingface/token.
func huggingFaceTokenFile(e setupEnv) string {
	getenv := homeEnvironment(e)

	switch {
	case filepath.IsAbs(getenv("HF_TOKEN_PATH")):
		return getenv("HF_TOKEN_PATH")
	case filepath.IsAbs(getenv("HF_HOME")):
		return filepath.Join(getenv("HF_HOME"), "token")
	case filepath.IsAbs(getenv("XDG_CACHE_HOME")):
		return filepath.Join(getenv("XDG_CACHE_HOME"), "huggingface", "token")
	}

	return ".cache/huggingface/token"
}

func stepPath(e setupEnv, client setupClient, file string) string {
	if filepath.IsAbs(file) {
		return file
	}

	if client.system {
		return filepath.Join(e.root, file)
	}

	return filepath.Join(e.home, file)
}

var (
	plainShellWord  = regexp.MustCompile(`^[A-Za-z0-9@%+=:,./_-]+$`)
	shellAssignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
)

// shellWord is word as a POSIX shell reads it back unchanged: quoted unless
// it is plain, with only the value of a NAME=value assignment quoted.
func shellWord(word string) string {
	if plainShellWord.MatchString(word) {
		return word
	}

	if name := shellAssignment.FindString(word); name != "" {
		return name + shellWord(strings.TrimPrefix(word, name))
	}

	return "'" + strings.ReplaceAll(word, "'", `'\''`) + "'"
}

func printSetupStep(w io.Writer, e setupEnv, client setupClient, s setupStep) {
	if s.command != nil {
		words := make([]string, 0, len(s.env)+len(s.command)+len(s.argumentFile))
		for _, word := range append(append(slices.Clone(s.env), s.command...), s.argumentFile...) {
			words = append(words, shellWord(word))
		}

		cmdline := strings.Join(words, " ")
		if s.stdin != "" {
			cmdline = "echo " + shellWord(strings.TrimSuffix(s.stdin, "\n")) + " | " + cmdline
		}

		if s.undo {
			cmdline += "   # when an earlier setup added it"
		}

		_, _ = fmt.Fprintf(w, "$ %s\n", cmdline)

		return
	}

	what := ""
	if s.block {
		what = " (a block; the rest of the file stays)"
	}

	_, _ = fmt.Fprintf(w, "# %s%s\n%s", stepPath(e, client, s.file), what, s.content)

	if !strings.HasSuffix(s.content, "\n") {
		_, _ = fmt.Fprintln(w)
	}
}

func blockMarkers(repo string) (begin, end string) {
	return "# relic setup " + repo + " begin", "# relic setup " + repo + " end"
}

func withBlock(old, repo, content string) (string, error) {
	begin, end := blockMarkers(repo)
	block := begin + "\n" + strings.TrimSuffix(content, "\n") + "\n" + end + "\n"

	if i, _ := markerLine(old, begin); i >= 0 {
		_, after := markerLine(old[i:], end)
		if after < 0 {
			return "", fmt.Errorf("%q has no matching %q; fix the file by hand", begin, end)
		}

		return old[:i] + block + old[i+after:], nil
	}

	if old != "" && !strings.HasSuffix(old, "\n") {
		old += "\n"
	}

	return old + block, nil
}

var setupBlockBegin = regexp.MustCompile(`(?m)^# relic setup (\S+) begin\r?$`)

var yamlTopLevelKey = regexp.MustCompile(`(?m)^([A-Za-z0-9_-]+)\s*:`)

func withSoleBlock(old, repo, content string) (string, error) {
	kept := old

	for _, match := range setupBlockBegin.FindAllStringSubmatch(old, -1) {
		if match[1] == repo {
			continue
		}

		var err error
		if kept, err = withoutBlock(kept, match[1]); err != nil {
			return "", err
		}
	}

	outside, err := withoutBlock(kept, repo)
	if err != nil {
		return "", err
	}

	set := map[string]bool{}
	for _, match := range yamlTopLevelKey.FindAllStringSubmatch(outside, -1) {
		set[match[1]] = true
	}

	for _, match := range yamlTopLevelKey.FindAllStringSubmatch(content, -1) {
		if set[match[1]] {
			return "", fmt.Errorf(
				"%s is already set outside relic's block; remove it, or merge by hand (relic package setup without --write prints it)",
				match[1],
			)
		}
	}

	return withBlock(kept, repo, content)
}

func withoutBlock(old, repo string) (string, error) {
	begin, end := blockMarkers(repo)

	i, _ := markerLine(old, begin)
	if i < 0 {
		return old, nil
	}

	_, after := markerLine(old[i:], end)
	if after < 0 {
		return "", fmt.Errorf("%q has no matching %q; fix the file by hand", begin, end)
	}

	return old[:i] + old[i+after:], nil
}

var homeVariables = []string{
	"HOME",
	"USERPROFILE",
	"APPDATA",
	"LOCALAPPDATA",
	"XDG_CONFIG_HOME",
	"XDG_CACHE_HOME",
	"XDG_DATA_HOME",
	"XDG_STATE_HOME",
}

// setupCommandEnvironment is the environment client commands run in: this
// process's, or, for another home than the user's, one in which every
// per-user location the clients read is under home.
func setupCommandEnvironment(home string) []string {
	if home == "" || home == userHome() {
		return os.Environ()
	}

	env := slices.DeleteFunc(os.Environ(), func(variable string) bool {
		name, _, _ := strings.Cut(variable, "=")

		return slices.ContainsFunc(homeVariables, func(homeVariable string) bool {
			return strings.EqualFold(name, homeVariable)
		})
	})

	env = append(env, "HOME="+home)
	if runtime.GOOS == "windows" {
		env = append(
			env,
			"USERPROFILE="+home,
			"APPDATA="+filepath.Join(home, "AppData", "Roaming"),
			"LOCALAPPDATA="+filepath.Join(home, "AppData", "Local"),
		)
	}

	return env
}

func applySetupStep(cmd *cobra.Command, e setupEnv, client setupClient, s setupStep, force bool) error {
	out := cmd.OutOrStdout()

	if s.command != nil {
		args := slices.Clone(s.command[1:])

		if s.argumentFile != nil {
			path, err := writeArgumentFile(s.argumentFile)
			if err != nil {
				return fmt.Errorf("setup: %s: %w", s.command[0], err)
			}

			defer func() {
				_ = os.Remove(path)
			}()

			args = append(args, "@"+path)
		}

		//nolint:gosec // G204: the client's own command, arguments not parsed by a shell
		c := exec.CommandContext(cmd.Context(), s.command[0], args...)
		c.Stdin = strings.NewReader(s.stdin)
		c.Env = append(setupCommandEnvironment(e.home), s.env...)
		c.Stdout, c.Stderr = cmd.ErrOrStderr(), cmd.ErrOrStderr()

		if s.undo {
			c.Stdout, c.Stderr = io.Discard, io.Discard
		}

		err := c.Run()

		switch {
		case s.undo:
			return nil
		case err != nil:
			return fmt.Errorf("setup: %s: %w", s.command[0], err)
		}

		_, _ = fmt.Fprintf(out, "ran %s\n", strings.Join(append(slices.Clone(s.command), s.argumentFile...)[:2], " "))

		return nil
	}

	path := stepPath(e, client, s.file)

	old, err := os.ReadFile(path) //nolint:gosec // the client's configuration file
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	content := s.content

	switch {
	case s.soleBlock:
		content, err = withSoleBlock(string(old), e.repo, s.content)
		if err != nil {
			return fmt.Errorf("setup: %s: %w", path, err)
		}
	case s.block:
		content, err = withBlock(string(old), e.repo, s.content)
		if err != nil {
			return fmt.Errorf("setup: %s: %w", path, err)
		}
	case err == nil && string(old) != s.content && !force && !s.owned:
		return fmt.Errorf(
			"setup: %s exists; merge it by hand (relic package setup without --write prints it) or replace it with --force",
			path,
		)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	if err := writeFileAtomic(path, strings.NewReader(content), s.mode); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(out, "wrote %s\n", path)

	return nil
}
