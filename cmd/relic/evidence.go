package main

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/delivery/evidence"

	"relic/internal/platform/terminal"
)

func evidenceCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "evidence",
		Short: "Sign, store, show and verify evidence about files",
		Long: "Evidence is a signed in-toto statement about a file (a test report, an approval, a scan), " +
			"stored beside it in a DSSE envelope and verified against the trusted keys.",
	}
	cmd.AddCommand(
		evidenceKeygenCmd(g),
		evidenceCreateCmd(g),
		evidenceGetCmd(g),
		evidenceVerifyCmd(g),
	)

	return cmd
}

func evidenceKeygenCmd(g *globals) *cobra.Command {
	var (
		keyType, file, alias string
		force                bool
	)

	cmd := &cobra.Command{
		Use:   "keygen",
		Short: "Generate a key pair for signing evidence, and trust it on the server",
		Long: "Writes <file>.key (the private key, PKCS#8 PEM, mode 0600) and <file>.pub (the public key). " +
			"With --alias the public key is also added to the server's trusted keys under that alias " +
			"(administrators), which evidence create then signs as.",
		Example: "  relic evidence keygen --file ci-signing --alias ci\n  relic evidence keygen --type ecdsa",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var key crypto.Signer

			var err error

			switch keyType {
			case "ed25519":
				_, key, err = ed25519.GenerateKey(rand.Reader)
			case "ecdsa":
				key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			case "rsa":
				key, err = rsa.GenerateKey(rand.Reader, 3072)
			default:
				return fmt.Errorf("unknown key type %q: use ed25519, ecdsa or rsa", keyType)
			}

			if err != nil {
				return err
			}

			priv, err := x509.MarshalPKCS8PrivateKey(key)
			if err != nil {
				return err
			}

			pub, err := x509.MarshalPKIXPublicKey(key.Public())
			if err != nil {
				return err
			}

			pubPEM := pem.EncodeToMemory(&pem.Block{
				Type:  "PUBLIC KEY",
				Bytes: pub,
			})

			privPEM := pem.EncodeToMemory(&pem.Block{
				Type:  "PRIVATE KEY",
				Bytes: priv,
			})

			if err := writeKeyPair(file, privPEM, pubPEM, force); err != nil {
				return fmt.Errorf("keygen: %w", err)
			}

			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s.key and %s.pub written\n", file, file)

			if alias == "" {
				return nil
			}

			c, err := g.dial()
			if err != nil {
				return err
			}

			r, err := c.evidence.AddTrustedKey(
				cmd.Context(),
				connect.NewRequest(&relicv1.AddTrustedKeyRequest{
					Alias:     alias,
					PublicKey: string(pubPEM),
				}),
			)
			if err != nil {
				return fmt.Errorf("keygen: trust the key: %w", err)
			}

			_, _ = fmt.Fprintf(
				cmd.OutOrStdout(),
				"trusted as %s (fingerprint %s)\n",
				r.Msg.GetKey().GetAlias(),
				r.Msg.GetKey().GetFingerprint(),
			)

			return nil
		},
	}
	cmd.Flags().StringVar(&keyType, "type", "ed25519", "key type: ed25519, ecdsa (P-256) or rsa")
	cmd.Flags().StringVar(&file, "file", "evidence", "file name prefix of the key pair")
	cmd.Flags().StringVar(&alias, "alias", "", "add the public key to the server's trusted keys under this alias")
	cmd.Flags().BoolVar(&force, "force", false, "replace existing key files")

	return cmd
}

type statement struct {
	Type          string             `json:"_type"`
	Subject       []statementSubject `json:"subject"`
	PredicateType string             `json:"predicateType"`
	Predicate     json.RawMessage    `json:"predicate"`
}

type statementSubject struct {
	Name   string            `json:"name"`
	Digest map[string]string `json:"digest"`
}

func evidenceCreateCmd(g *globals) *cobra.Command {
	var (
		predicateFile, predicateType, keyFile, alias, provider string
		output                                                 string
	)

	cmd := &cobra.Command{
		Use:   "create <repository>/<path>",
		Short: "Sign a statement about a file and store it as evidence",
		Long: "Builds an in-toto statement whose subject is the file (its SHA-256 read from the server) and " +
			"whose predicate is --predicate, signs it with --key as --alias (a trusted key's alias), and " +
			"stores the envelope beside the file. The server refuses evidence signed by an untrusted alias.",
		Example: "  relic evidence create libs-local/app/1.0/app.jar --predicate tests.json \\\n" +
			"    --predicate-type https://relic.build/evidence/tests/v1 --key ci-signing.key --alias ci",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeTargets(g),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, p, err := splitTarget(args[0])
			if err != nil {
				return err
			}

			predicate, err := os.ReadFile(predicateFile) //nolint:gosec // a file the user names
			if err != nil {
				return err
			}

			if !json.Valid(predicate) {
				return fmt.Errorf("%s is not JSON", predicateFile)
			}

			pemKey, err := os.ReadFile(keyFile) //nolint:gosec // a file the user names
			if err != nil {
				return err
			}

			key, err := evidence.ParsePrivateKey(pemKey)
			if err != nil {
				return fmt.Errorf("%s: %w", keyFile, err)
			}

			c, err := g.dial()
			if err != nil {
				return err
			}

			st, err := c.artifacts.StatItem(
				cmd.Context(),
				connect.NewRequest(&relicv1.StatItemRequest{
					Repository: repo,
					Path:       p,
				}),
			)
			if err != nil {
				return fmt.Errorf("evidence create: %w", err)
			}

			payload, err := json.Marshal(statement{
				Type: "https://in-toto.io/Statement/v1",
				Subject: []statementSubject{
					{
						Name: p,
						Digest: map[string]string{
							"sha256": st.Msg.GetItem().GetChecksums().GetSha256(),
						},
					},
				},
				PredicateType: predicateType,
				Predicate:     predicate,
			})
			if err != nil {
				return err
			}

			env, err := evidence.Sign(key, alias, evidence.StatementType, payload)
			if err != nil {
				return err
			}

			envelope, err := json.Marshal(env)
			if err != nil {
				return err
			}

			r, err := c.evidence.CreateEvidence(
				cmd.Context(),
				connect.NewRequest(&relicv1.CreateEvidenceRequest{
					Repository: repo,
					Path:       p,
					Envelope:   string(envelope),
					Provider:   provider,
				}),
			)
			if err != nil {
				return fmt.Errorf("evidence create: %w", err)
			}

			ev := r.Msg.GetEvidence()

			return printMessage(cmd.OutOrStdout(), output, ev, func(w io.Writer) error {
				_, err := fmt.Fprintf(w, "%s/%s: evidence %s (%s) stored, verified %v\n", repo, p, ev.GetId(),
					ev.GetPredicateSlug(), ev.GetVerified())

				return err
			})
		},
	}
	cmd.Flags().StringVar(&predicateFile, "predicate", "", "the predicate, a JSON file")
	cmd.Flags().StringVar(&predicateType, "predicate-type", "", "the predicate's type URI")
	cmd.Flags().StringVar(&keyFile, "key", "", "the private key (PEM) to sign with")
	cmd.Flags().StringVar(&alias, "alias", "", "the trusted key's alias the signature names")
	cmd.Flags().StringVar(&provider, "provider", "relic", "what produced the evidence")
	outputFlag(cmd, &output)

	for _, f := range []string{
		"predicate",
		"predicate-type",
		"key",
		"alias",
	} {
		_ = cmd.MarkFlagRequired(f)
	}

	return cmd
}

func (c *client) listEvidence(cmd *cobra.Command, target string) (string, string, []*relicv1.Evidence, error) {
	repo, p, err := splitTarget(target)
	if err != nil {
		return "", "", nil, err
	}

	r, err := c.evidence.ListEvidence(
		cmd.Context(),
		connect.NewRequest(&relicv1.ListEvidenceRequest{
			Repository: repo,
			Path:       p,
		}),
	)
	if err != nil {
		return "", "", nil, fmt.Errorf("evidence: %w", err)
	}

	return repo, p, r.Msg.GetEvidence(), nil
}

func evidenceGetCmd(g *globals) *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:               "get <repository>/<path>",
		Short:             "Show the evidence about a file",
		Example:           "  relic evidence get libs-local/app/1.0/app.jar -o json",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeTargets(g),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.dial()
			if err != nil {
				return err
			}

			_, _, evs, err := c.listEvidence(cmd, args[0])
			if err != nil {
				return err
			}

			return printMessage(cmd.OutOrStdout(), output, &relicv1.ListEvidenceResponse{
				Evidence: evs,
			}, func(w io.Writer) error {
				table := terminal.Table{
					Headers: []string{
						"ID",
						"PREDICATE",
						"SIGNED BY",
						"VERIFIED",
						"CREATED",
						"BY",
					},
				}

				for _, e := range evs {
					verified := "no"
					if e.GetVerified() {
						verified = "yes"
					}

					table.Rows = append(table.Rows, []string{
						e.GetId(),
						e.GetPredicateType(),
						e.GetKeyAlias(),
						verified,
						localTime(e.GetCreated(), time.DateTime),
						e.GetCreatedBy(),
					})
				}

				return table.Print(w)
			})
		},
	}
	outputFlag(cmd, &output)

	return cmd
}

func evidenceVerifyCmd(g *globals) *cobra.Command {
	var publicKeys []string

	cmd := &cobra.Command{
		Use:   "verify <repository>/<path>",
		Short: "Verify the evidence about a file; exit 1 unless all of it verifies",
		Long: "Downloads each envelope and checks its signature here, with --public-key files or, without " +
			"them, the server's trusted keys (by the alias the signature names), and that its subject is " +
			"the file's current SHA-256. It does not rely on the server's own verdict.",
		Example:           "  relic evidence verify libs-local/app/1.0/app.jar --public-key ci-signing.pub",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeTargets(g),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.dial()
			if err != nil {
				return err
			}

			repo, p, evs, err := c.listEvidence(cmd, args[0])
			if err != nil {
				return err
			}

			if len(evs) == 0 {
				return fmt.Errorf("%s/%s has no evidence", repo, p)
			}

			st, err := c.artifacts.StatItem(
				cmd.Context(),
				connect.NewRequest(&relicv1.StatItemRequest{
					Repository: repo,
					Path:       p,
				}),
			)
			if err != nil {
				return err
			}

			keys, err := verificationKeys(cmd, c, publicKeys)
			if err != nil {
				return err
			}

			failed := 0
			table := terminal.Table{
				Headers: []string{
					"ID",
					"PREDICATE",
					"RESULT",
					"DETAIL",
				},
			}

			for _, e := range evs {
				g.progress.describe("Verifying " + e.GetId())

				result, detail := "verified", "signed by "+e.GetKeyAlias()
				if reason := verifyEvidence(
					cmd,
					c,
					e,
					keys,
					st.Msg.GetItem().GetChecksums().GetSha256(),
				); reason != "" {
					failed++
					result, detail = "FAILED", reason
				}

				table.Rows = append(table.Rows, []string{
					e.GetId(),
					e.GetPredicateType(),
					result,
					tableCell(detail),
				})
			}

			if err := table.Print(cmd.OutOrStdout()); err != nil {
				return err
			}

			if failed > 0 {
				return exitCode1
			}

			return nil
		},
	}
	cmd.Flags().StringArrayVar(&publicKeys, "public-key", nil, "verify with this public key (PEM file; repeatable)")

	return cmd
}

func writeKeyPair(file string, privPEM, pubPEM []byte, force bool) error {
	names := []string{
		file + ".key",
		file + ".pub",
	}

	if !force {
		for _, name := range names {
			if _, err := os.Lstat(name); err == nil {
				return fmt.Errorf("%s exists (--force replaces it)", name)
			} else if !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
	}

	var staged []string

	defer func() {
		for _, name := range staged {
			_ = os.Remove(name)
		}
	}()

	for _, content := range [][]byte{
		privPEM,
		pubPEM,
	} {
		tmp, err := os.CreateTemp(filepath.Dir(file), ".relic-key-*")
		if err != nil {
			return err
		}

		staged = append(staged, tmp.Name())

		if _, err := tmp.Write(content); err != nil {
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
	}

	for i, name := range names {
		if err := os.Rename(staged[i], name); err != nil {
			return err
		}
	}

	return nil
}

func verificationKeys(cmd *cobra.Command, c *client, files []string) (map[string][]crypto.PublicKey, error) {
	out := map[string][]crypto.PublicKey{}

	for _, f := range files {
		b, err := os.ReadFile(f) //nolint:gosec // a file the user names
		if err != nil {
			return nil, err
		}

		k, _, err := evidence.ParsePublicKey(string(b))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}

		out[""] = append(out[""], k)
	}

	if len(files) > 0 {
		return out, nil
	}

	r, err := c.evidence.ListTrustedKeys(cmd.Context(), connect.NewRequest(&relicv1.ListTrustedKeysRequest{}))
	if err != nil {
		return nil, fmt.Errorf("trusted keys: %w", err)
	}

	for _, t := range r.Msg.GetKeys() {
		k, _, err := evidence.ParsePublicKey(t.GetPublicKey())
		if err != nil {
			continue
		}

		out[t.GetAlias()] = append(out[t.GetAlias()], k)
	}

	return out, nil
}

func verifyEvidence(
	cmd *cobra.Command,
	c *client,
	e *relicv1.Evidence,
	keys map[string][]crypto.PublicKey,
	sha256 string,
) string {
	req, err := http.NewRequestWithContext(
		cmd.Context(),
		http.MethodGet,
		c.fileURL(evidence.Repository, e.GetStoredPath()),
		nil,
	)
	if e.GetStoredPath() == "" || err != nil {
		return "the envelope is not stored"
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err.Error()
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return "download the envelope: " + resp.Status
	}

	var env evidence.Envelope
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&env); err != nil || len(env.Signatures) == 0 {
		return "not a DSSE envelope"
	}

	candidates := slices.Clone(keys[""])
	for _, s := range env.Signatures {
		candidates = append(candidates, keys[s.KeyID]...)
	}

	if len(candidates) == 0 {
		return "no key for " + env.Signatures[0].KeyID
	}

	ok := false

	for _, k := range candidates {
		ok = ok || evidence.Verify(k, env)
	}

	if !ok {
		return "the signature does not verify"
	}

	payload, err := evidencePayload(env)
	if err != nil {
		return err.Error()
	}

	for _, s := range payload.Subject {
		if s.Digest["sha256"] == sha256 {
			return ""
		}
	}

	return "the statement is about other content than the file holds now"
}

func evidencePayload(env evidence.Envelope) (statement, error) {
	var st statement

	b, err := base64.StdEncoding.DecodeString(env.Payload)
	if err != nil {
		return st, errors.New("the payload is not base64")
	}

	if err := json.Unmarshal(b, &st); err != nil {
		return st, errors.New("the payload is not an in-toto statement")
	}

	return st, nil
}
