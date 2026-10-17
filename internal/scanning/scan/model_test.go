package scan

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func short(s string) []byte {
	return append([]byte{
		0x8c,
		byte(len(s)), //nolint:gosec // short test strings
	}, s...)
}

func protocol4(parts ...[]byte) []byte {
	body := bytes.Join(parts, nil)

	return append([]byte{
		0x80,
		4,
	}, append(body, '.')...)
}

func npy(payload []byte) []byte {
	header := []byte("{'descr': '|O', 'fortran_order': False, 'shape': (1,), }")
	header = append(header, bytes.Repeat([]byte(" "), 64-(10+len(header)+1)%64)...)
	header = append(header, '\n')

	out := []byte("\x93NUMPY\x01\x00")
	out = binary.LittleEndian.AppendUint16(out, uint16(len(header))) //nolint:gosec // a 64-byte header
	out = append(out, header...)

	return append(out, payload...)
}

func largeHeaderNPY(payload []byte) []byte {
	header := []byte("{'descr': '|O', 'fortran_order': False, 'shape': (1,), }")
	header = append(header, bytes.Repeat([]byte(" "), 2<<20)...)
	header = append(header, '\n')

	out := []byte("\x93NUMPY\x02\x00")
	out = binary.LittleEndian.AppendUint32(out, uint32(len(header))) //nolint:gosec // a 2 MiB header
	out = append(out, header...)

	return append(out, payload...)
}

func torchArchive(t *testing.T, pickle []byte) []byte {
	t.Helper()

	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)
	for name, body := range map[string][]byte{
		"archive/data.pkl": pickle,
		"archive/data/0": bytes.Repeat([]byte{
			0,
		}, 16),
		"archive/version": []byte("3\n"),
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}

		if _, err := w.Write(body); err != nil {
			t.Fatal(err)
		}
	}

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func TestModelAnalyzer(t *testing.T) {
	t.Parallel()

	tensor := []byte(
		"\x80\x02ctorch._utils\n_rebuild_tensor_v2\nq\x00(ccollections\nOrderedDict\nq\x01)Rq\x02tq\x03Rq\x04.",
	)

	tests := []struct {
		name     string
		file     string
		body     []byte
		rule     string
		severity Severity
		resource string
	}{
		{
			name:     "os.system in protocol 0",
			file:     "model.pkl",
			body:     []byte("cos\nsystem\n(S'echo pwned'\ntR."),
			rule:     RuleUnsafeImport,
			severity: SeverityCritical,
			resource: "os.system",
		},
		{
			name: "eval through a memoized stack global",
			file: "model.joblib",
			body: protocol4(
				short("builtins"),
				[]byte{
					0x94,
				},
				short("eval"),
				[]byte{
					0x94,
					0x93,
				},
				short("print(1)"),
				[]byte{
					0x85,
					'R',
				},
			),
			rule:     RuleUnsafeImport,
			severity: SeverityCritical,
			resource: "builtins.eval",
		},
		{
			name: "a stack global through the memo",
			file: "model.pkl",
			body: protocol4(
				short("subprocess"),
				[]byte{
					0x94,
				},
				short("Popen"),
				[]byte{
					0x94,
					'0',
					'0',
					'h',
					0,
					'h',
					1,
					0x93,
				},
			),
			rule:     RuleUnsafeImport,
			severity: SeverityCritical,
			resource: "subprocess.Popen",
		},
		{
			name: "a computed global",
			file: "model.pkl",
			body: protocol4(
				[]byte{
					'K',
					1,
					'K',
					2,
					0x93,
				},
			),
			rule:     RuleDynamicImport,
			severity: SeverityHigh,
			resource: "dynamic",
		},
		{
			name:     "an unknown module",
			file:     "model.dill",
			body:     []byte("cmyproject.layers\nCustom\n)R."),
			rule:     RuleUnknownImport,
			severity: SeverityMedium,
			resource: "myproject.layers.Custom",
		},
		{
			name:     "os.system in a PyTorch archive",
			file:     "model.pt",
			body:     torchArchive(t, []byte("\x80\x02cposix\nsystem\nX\x02\x00\x00\x00idtR.")),
			rule:     RuleUnsafeImport,
			severity: SeverityCritical,
			resource: "posix.system",
		},
		{
			name:     "os.system in a NumPy object array",
			file:     "weights.npy",
			body:     npy([]byte("\x80\x02cos\nsystem\nX\x02\x00\x00\x00idtR.")),
			rule:     RuleUnsafeImport,
			severity: SeverityCritical,
			resource: "os.system",
		},
		{
			name:     "os.system after a NumPy header over 1 MiB",
			file:     "weights.npy",
			body:     largeHeaderNPY([]byte("\x80\x02cos\nsystem\nX\x02\x00\x00\x00idtR.")),
			rule:     RuleUnsafeImport,
			severity: SeverityCritical,
			resource: "os.system",
		},
		{
			name:     "os.system in a pickle cut short",
			file:     "model.pkl",
			body:     []byte("cos\nsystem\n(S'id'\ntR"),
			rule:     RuleUnsafeImport,
			severity: SeverityCritical,
			resource: "os.system",
		},
		{
			name: "a pickle that memoizes without end",
			file: "model.pkl",
			body: protocol4(
				short("x"),
				bytes.Repeat([]byte{
					0x94,
				}, maxPickleMemo+1),
			),
			rule:     RuleDynamicImport,
			severity: SeverityMedium,
		},
		{
			name: "a PyTorch archive whose pickle unpacks past the limit",
			file: "model.pt",
			body: torchArchive(t, protocol4(
				bytes.Repeat([]byte{
					'N',
				}, maxPickleEntry+1),
			)),
			rule:     RuleDynamicImport,
			severity: SeverityMedium,
		},
		{
			name: "a plain PyTorch archive",
			file: "model.pth",
			body: torchArchive(t, tensor),
		},
		{
			name: "a legacy PyTorch file of several pickles and raw storage",
			file: "model.bin",
			body: append(
				append([]byte("\x80\x02\x8a\x0a\x6c\xfc\x9c\x46\xf9\x20\x6a\xa8\x50\x19."), tensor...),
				0xff,
				0x00,
				0x13,
			),
		},
		{
			name: "a binary that is no pickle",
			file: "model.bin",
			body: []byte{
				0x7f,
				'E',
				'L',
				'F',
				2,
			},
		},
		{
			name: "a file Model does not read",
			file: "model.onnx",
			body: []byte("cos\nsystem\n."),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), tt.file)
			if err := os.WriteFile(path, tt.body, 0o600); err != nil {
				t.Fatal(err)
			}

			issues, err := Model{}.Analyze(t.Context(), "file:"+path)
			if err != nil {
				t.Fatal(err)
			}

			if tt.rule == "" {
				if len(issues) != 0 {
					t.Errorf("issues = %+v, want none", issues)
				}

				return
			}

			if len(issues) != 1 {
				t.Fatalf("issues = %+v, want one", issues)
			}

			got := issues[0]
			if got.Analyzer != AnalyzerModel || got.Rule != tt.rule || got.Severity != tt.severity ||
				got.Resource != tt.resource {
				t.Errorf("issue = %+v, want %s %s %s", got, tt.rule, tt.severity, tt.resource)
			}
		})
	}
}

func TestReadLine(t *testing.T) {
	t.Parallel()

	unterminated := bufio.NewReader(strings.NewReader(strings.Repeat("a", maxPickleString+1)))
	if _, err := readLine(unterminated); err == nil || !strings.Contains(err.Error(), "too long") {
		t.Errorf("a line over the limit without a newline: err = %v, want it refused", err)
	}

	lines := bufio.NewReader(strings.NewReader("os\r\nsystem\n"))
	if line, err := readLine(lines); err != nil || line != "os" {
		t.Errorf("line = %q, %v; want os", line, err)
	}
}

func TestModelAnalyzerWalksDirectories(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "models"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(
		filepath.Join(dir, "models", "evil.pkl"),
		[]byte("cnt\nsystem\n(S'x'\ntR."),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	issues, err := Model{}.Analyze(t.Context(), "dir:"+dir)
	if err != nil || len(issues) != 1 || issues[0].File != "models/evil.pkl" || issues[0].Resource != "nt.system" ||
		issues[0].Rule != RuleUnsafeImport ||
		issues[0].Severity != SeverityCritical {
		t.Errorf("issues = %+v, %v; want nt.system as a critical unsafe import", issues, err)
	}
}

func TestModelAnalyzerStopsWhenCanceled(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "model.pt")
	if err := os.WriteFile(path, torchArchive(t, []byte("\x80\x02N.")), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := (Model{}).Analyze(ctx, "file:"+path); !errors.Is(err, context.Canceled) {
		t.Errorf("Analyze with a canceled context = %v, want context.Canceled", err)
	}
}
