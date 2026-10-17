package scan

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// AnalyzerModel finds code that loading a machine learning model would run:
// the imports of its pickle streams.
const AnalyzerModel = "model"

// Model rules.
const (
	RuleUnsafeImport  = "pickle-unsafe-import"
	RuleUnknownImport = "pickle-unknown-import"
	RuleDynamicImport = "pickle-dynamic-import"
)

const (
	maxPickleString = 64 << 20
	maxPickleMemo   = 1 << 20
	maxPickleEntry  = 64 << 20
)

// Model finds the imports of pickle streams in model files: .pkl, .pickle,
// .joblib, .dill, PyTorch archives (.pt, .pth, .bin, .ckpt) and NumPy object
// arrays (.npy). It runs in process, with no scanner to install.
type Model struct{}

// Name implements Analyzer.
func (Model) Name() string { return AnalyzerModel }

var modelExtensions = []string{
	".pkl",
	".pickle",
	".joblib",
	".dill",
	".pt",
	".pth",
	".bin",
	".ckpt",
	".npy",
}

// IsModelFile reports whether name is a file Model reads.
func IsModelFile(name string) bool {
	return slices.Contains(modelExtensions, strings.ToLower(filepath.Ext(name)))
}

// Analyze implements Analyzer.
func (m Model) Analyze(ctx context.Context, source string) ([]Issue, error) {
	kind, root, _ := strings.Cut(source, ":")

	switch kind {
	case "file":
		return m.analyzeFile(ctx, root, filepath.Base(root))
	case "dir":
		var issues []Issue

		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !IsModelFile(p) {
				return err
			}

			if err := ctx.Err(); err != nil {
				return err
			}

			rel, _ := filepath.Rel(root, p)

			found, err := m.analyzeFile(ctx, p, filepath.ToSlash(rel))
			if err != nil {
				return err
			}

			issues = append(issues, found...)

			return nil
		})

		return issues, err
	}

	return nil, nil
}

func (Model) analyzeFile(ctx context.Context, path, name string) ([]Issue, error) {
	if !IsModelFile(name) {
		return nil, nil
	}

	f, err := os.Open(path) //nolint:gosec // a file of the content being scanned
	if err != nil {
		return nil, err
	}

	defer func() {
		_ = f.Close()
	}()

	head := make([]byte, 8)
	n, _ := io.ReadFull(f, head)
	head = head[:n]

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	switch {
	case bytes.HasPrefix(head, []byte("PK\x03\x04")):
		return zipImports(ctx, path, name)
	case bytes.HasPrefix(head, []byte("\x93NUMPY")):
		return npyImports(f, name)
	}

	imports, err := pickleImports(bufio.NewReader(&pickleReader{
		ctx:   ctx,
		r:     f,
		limit: -1,
	}), true)

	switch {
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case errors.Is(err, errPickleMemo):
		return append(importIssues(imports, name), unreadablePickle(name, err)), nil
	}

	return importIssues(imports, name), nil
}

func unreadablePickle(file string, err error) Issue {
	return Issue{
		Analyzer: AnalyzerModel,
		Rule:     RuleDynamicImport,
		Severity: SeverityMedium,
		Title:    "unreadable pickle stream: " + err.Error(),
		File:     file,
	}
}

var (
	errPickleMemo  = fmt.Errorf("the pickle memoizes more than %d values", maxPickleMemo)
	errPickleEntry = fmt.Errorf("the pickle unpacks to more than %d bytes", maxPickleEntry)
)

type pickleReader struct {
	ctx   context.Context
	r     io.Reader
	limit int64
}

func (p *pickleReader) Read(b []byte) (int, error) {
	if err := p.ctx.Err(); err != nil {
		return 0, err
	}

	if p.limit == 0 {
		return 0, errPickleEntry
	}

	if p.limit > 0 && int64(len(b)) > p.limit {
		b = b[:p.limit]
	}

	n, err := p.r.Read(b)
	if p.limit > 0 {
		p.limit -= int64(n)
	}

	return n, err
}

func zipImports(ctx context.Context, path, name string) ([]Issue, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, nil //nolint:nilerr // not a readable archive
	}

	defer func() {
		_ = zr.Close()
	}()

	var issues []Issue

	for _, f := range zr.File {
		if !strings.HasSuffix(f.Name, ".pkl") {
			continue
		}

		rc, err := f.Open()
		if err != nil {
			continue
		}

		imports, err := pickleImports(bufio.NewReader(&pickleReader{
			ctx:   ctx,
			r:     rc,
			limit: maxPickleEntry,
		}), false)
		_ = rc.Close()

		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		issues = append(issues, importIssues(imports, name+"/"+f.Name)...)

		if err != nil {
			issues = append(issues, unreadablePickle(name+"/"+f.Name, err))
		}
	}

	return issues, nil
}

func npyImports(r io.Reader, name string) ([]Issue, error) {
	br := bufio.NewReader(r)

	magic := make([]byte, 8)
	if _, err := io.ReadFull(br, magic); err != nil {
		return nil, nil //nolint:nilerr // a truncated array holds no pickle
	}

	var headerLen int

	switch magic[6] {
	case 1:
		var n uint16
		if err := binary.Read(br, binary.LittleEndian, &n); err != nil {
			return nil, nil //nolint:nilerr // a truncated header holds no pickle
		}

		headerLen = int(n)
	default:
		var n uint32
		if err := binary.Read(br, binary.LittleEndian, &n); err != nil {
			return nil, nil //nolint:nilerr // a truncated header holds no pickle
		}

		headerLen = int(n)
	}

	header := make([]byte, min(headerLen, 1<<20))
	if _, err := io.ReadFull(br, header); err != nil {
		return nil, nil //nolint:nilerr // a truncated header holds no pickle
	}

	if _, err := br.Discard(headerLen - len(header)); err != nil {
		return nil, nil //nolint:nilerr // a truncated header holds no pickle
	}

	if headerLen == len(header) && !bytes.Contains(header, []byte("'|O'")) &&
		!bytes.Contains(header, []byte("'O'")) {
		return nil, nil
	}

	imports, err := pickleImports(br, false)
	if err != nil {
		return nil, nil //nolint:nilerr // object data that is not a pickle runs nothing
	}

	return importIssues(imports, name), nil
}

// pickleImport is a global a pickle stream loads; dynamic marks one whose
// name the stream computes.
type pickleImport struct {
	module, name string
	dynamic      bool
}

var errNotPickle = errors.New("not a pickle stream")

// pickleImports walks the opcodes of the pickle streams in r, one after
// another as PyTorch's legacy format writes them, and returns the globals
// they import.
func pickleImports(r *bufio.Reader, several bool) ([]pickleImport, error) {
	var imports []pickleImport

	for i := 0; ; i++ {
		found, err := walkPickle(r)
		imports = append(imports, found...)

		if err != nil && i == 0 {
			return imports, err
		}

		if err != nil || !several || !hasMore(r) {
			return imports, nil //nolint:nilerr // after a legacy file's pickles come raw tensor bytes
		}
	}
}

func hasMore(r *bufio.Reader) bool {
	_, err := r.Peek(1)

	return err == nil
}

// stackValue is what walkPickle knows of a pushed value: a string, or not.
type stackValue struct {
	text     string
	isString bool
}

//nolint:gocyclo,cyclop,funlen // one case per opcode
func walkPickle(r *bufio.Reader) ([]pickleImport, error) {
	var (
		imports []pickleImport
		stack   []stackValue
		memo    = map[uint64]stackValue{}
	)

	push := func(v stackValue) {
		stack = append(stack, v)
		if len(stack) > 64 {
			stack = stack[1:]
		}
	}
	top := func() stackValue {
		if len(stack) == 0 {
			return stackValue{}
		}

		return stack[len(stack)-1]
	}
	other := func() {
		push(stackValue{})
	}

	memoize := func(idx uint64) error {
		if _, ok := memo[idx]; !ok && len(memo) >= maxPickleMemo {
			return errPickleMemo
		}

		memo[idx] = top()

		return nil
	}

	for first := true; ; first = false {
		op, err := r.ReadByte()
		if err != nil {
			if first {
				return nil, errNotPickle
			}

			return imports, fmt.Errorf("truncated pickle: %w", err)
		}

		switch op {
		case '.':
			return imports, nil
		case '(', '0', '1', 'N', ')', ']', '}', 'a', 'b', 'd', 'e', 'l', 'o', 'R', 's', 't', 'u',
			0x81, 0x85, 0x86, 0x87, 0x88, 0x89, 0x8f, 0x90, 0x91, 0x92, 0x97, 0x98, 'Q':
			other()
		case '2':
			push(top())
		case 'F', 'I', 'L', 'P':
			if _, err := readLine(r); err != nil {
				return imports, err
			}

			other()
		case 'S', 'V':
			line, err := readLine(r)
			if err != nil {
				return imports, err
			}

			push(stackValue{
				text:     strings.Trim(line, `'"`),
				isString: true,
			})
		case 'J':
			if err := skip(r, 4); err != nil {
				return imports, err
			}

			other()
		case 'K', 0x82:
			if err := skip(r, 1); err != nil {
				return imports, err
			}

			other()
		case 'M', 0x83:
			if err := skip(r, 2); err != nil {
				return imports, err
			}

			other()
		case 0x84:
			if err := skip(r, 4); err != nil {
				return imports, err
			}

			other()
		case 'G':
			if err := skip(r, 8); err != nil {
				return imports, err
			}

			other()
		case 0x80:
			if err := skip(r, 1); err != nil {
				return imports, err
			}
		case 0x95:
			if err := skip(r, 8); err != nil {
				return imports, err
			}
		case 0x8a:
			if err := skipCounted(r, 1); err != nil {
				return imports, err
			}

			other()
		case 0x8b:
			if err := skipCounted(r, 4); err != nil {
				return imports, err
			}

			other()
		case 'T', 'X':
			s, err := readCounted(r, 4)
			if err != nil {
				return imports, err
			}

			push(s)
		case 'U', 0x8c:
			s, err := readCounted(r, 1)
			if err != nil {
				return imports, err
			}

			push(s)
		case 0x8d:
			s, err := readCounted(r, 8)
			if err != nil {
				return imports, err
			}

			push(s)
		case 'B':
			if err := skipCounted(r, 4); err != nil {
				return imports, err
			}

			other()
		case 'C':
			if err := skipCounted(r, 1); err != nil {
				return imports, err
			}

			other()
		case 0x8e, 0x96:
			if err := skipCounted(r, 8); err != nil {
				return imports, err
			}

			other()
		case 'c', 'i':
			module, err := readLine(r)
			if err != nil {
				return imports, err
			}

			name, err := readLine(r)
			if err != nil {
				return imports, err
			}

			imports = append(imports, pickleImport{
				module: module,
				name:   name,
			})

			other()
		case 0x93:
			imp := pickleImport{
				dynamic: true,
			}
			if len(stack) >= 2 && stack[len(stack)-2].isString && stack[len(stack)-1].isString {
				imp = pickleImport{
					module: stack[len(stack)-2].text,
					name:   stack[len(stack)-1].text,
				}
			}

			imports = append(imports, imp)

			other()
		case 'g':
			line, err := readLine(r)
			if err != nil {
				return imports, err
			}

			var idx uint64

			_, _ = fmt.Sscan(line, &idx)
			push(memo[idx])
		case 'h':
			b, err := r.ReadByte()
			if err != nil {
				return imports, err
			}

			push(memo[uint64(b)])
		case 'j':
			var idx uint32
			if err := binary.Read(r, binary.LittleEndian, &idx); err != nil {
				return imports, err
			}

			push(memo[uint64(idx)])
		case 'p':
			line, err := readLine(r)
			if err != nil {
				return imports, err
			}

			var idx uint64

			_, _ = fmt.Sscan(line, &idx)
			if err := memoize(idx); err != nil {
				return imports, err
			}
		case 'q':
			b, err := r.ReadByte()
			if err != nil {
				return imports, err
			}

			if err := memoize(uint64(b)); err != nil {
				return imports, err
			}
		case 'r':
			var idx uint32
			if err := binary.Read(r, binary.LittleEndian, &idx); err != nil {
				return imports, err
			}

			if err := memoize(uint64(idx)); err != nil {
				return imports, err
			}
		case 0x94:
			if err := memoize(uint64(len(memo))); err != nil {
				return imports, err
			}
		default:
			if first {
				return nil, errNotPickle
			}

			return imports, fmt.Errorf("unknown pickle opcode 0x%02x", op)
		}
	}
}

func readLine(r *bufio.Reader) (string, error) {
	var line []byte

	for {
		chunk, err := r.ReadSlice('\n')
		if len(line)+len(chunk) > maxPickleString {
			return "", errors.New("pickle line too long")
		}

		line = append(line, chunk...)

		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}

		if err != nil {
			return "", err
		}

		return strings.TrimRight(string(line), "\r\n"), nil
	}
}

func readLength(r *bufio.Reader, width int) (uint64, error) {
	buf := make([]byte, 8)
	if _, err := io.ReadFull(r, buf[:width]); err != nil {
		return 0, err
	}

	return binary.LittleEndian.Uint64(buf), nil
}

func readCounted(r *bufio.Reader, width int) (stackValue, error) {
	n, err := readLength(r, width)
	if err != nil {
		return stackValue{}, err
	}

	if n > maxPickleString {
		return stackValue{}, skip(r, n)
	}

	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return stackValue{}, err
	}

	return stackValue{
		text:     string(buf),
		isString: true,
	}, nil
}

func skipCounted(r *bufio.Reader, width int) error {
	n, err := readLength(r, width)
	if err != nil {
		return err
	}

	return skip(r, n)
}

func skip(r *bufio.Reader, n uint64) error {
	want := int64(min(n, 1<<62)) //nolint:gosec // bounded

	copied, err := io.CopyN(io.Discard, r, want)
	if err == nil && copied != want {
		return io.ErrUnexpectedEOF
	}

	return err
}

// unsafeModules run code or reach the system when a pickle imports from
// them.
var unsafeModules = []string{
	"os",
	"posix",
	"nt",
	"subprocess",
	"sys",
	"socket",
	"runpy",
	"shutil",
	"pty",
	"webbrowser",
	"importlib",
	"pickle",
	"_pickle",
	"dill",
	"marshal",
	"types",
	"code",
	"codeop",
	"ctypes",
	"commands",
	"multiprocessing",
	"asyncio",
	"requests",
	"urllib",
	"http",
	"ftplib",
	"smtplib",
	"telnetlib",
	"platform",
	"pip",
	"setuptools",
	"tempfile",
	"signal",
	"threading",
	"_thread",
	"operator",
	"io",
	"_io",
}

var unsafeBuiltins = []string{
	"eval",
	"exec",
	"compile",
	"__import__",
	"open",
	"getattr",
	"setattr",
	"delattr",
	"globals",
	"locals",
	"vars",
	"input",
	"breakpoint",
	"memoryview",
}

// safeModules are the modules model files import to rebuild their data.
var safeModules = []string{
	"torch",
	"numpy",
	"collections",
	"_codecs",
	"copyreg",
	"copy_reg",
	"builtins",
	"__builtin__",
	"datetime",
	"decimal",
	"fractions",
	"argparse",
	"pathlib",
	"enum",
	"typing",
	"re",
	"uuid",
	"sklearn",
	"scipy",
	"pandas",
	"joblib",
	"xgboost",
	"lightgbm",
	"catboost",
	"transformers",
	"tokenizers",
	"sentencepiece",
	"safetensors",
}

func moduleRoot(module string) string {
	root, _, _ := strings.Cut(module, ".")

	return root
}

func importIssues(imports []pickleImport, file string) []Issue {
	seen := map[string]bool{}

	var issues []Issue

	for _, imp := range imports {
		global := imp.module + "." + imp.name

		var is Issue

		switch {
		case imp.dynamic:
			is = Issue{
				Rule:     RuleDynamicImport,
				Severity: SeverityHigh,
				Title:    "imports a global whose name the stream computes",
			}
			global = "dynamic"
		case (imp.module == "builtins" || imp.module == "__builtin__") && slices.Contains(unsafeBuiltins, imp.name),
			slices.Contains(unsafeModules, moduleRoot(imp.module)):
			is = Issue{
				Rule:     RuleUnsafeImport,
				Severity: SeverityCritical,
				Title:    "loading runs " + global,
			}
		case slices.Contains(safeModules, moduleRoot(imp.module)):
			continue
		default:
			is = Issue{
				Rule:     RuleUnknownImport,
				Severity: SeverityMedium,
				Title:    "imports " + global + ", which model files do not usually need",
			}
		}

		if seen[global] {
			continue
		}

		seen[global] = true
		is.Analyzer, is.File, is.Resource = AnalyzerModel, file, global
		issues = append(issues, is)
	}

	return issues
}
