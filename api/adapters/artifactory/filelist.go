package artifactory

import (
	"bytes"
	"encoding/json"
	"net/http"
)

const fileListFlushEntries = 1000

type fileListWriter struct {
	w       http.ResponseWriter
	buf     bytes.Buffer
	pending int
	entries int
	started bool
}

func newFileListWriter(w http.ResponseWriter, uri, created string) (*fileListWriter, error) {
	l := &fileListWriter{
		w: w,
	}

	uriJSON, err := json.Marshal(uri)
	if err != nil {
		return nil, err
	}

	createdJSON, err := json.Marshal(created)
	if err != nil {
		return nil, err
	}

	l.buf.WriteString("{\n  \"uri\": ")
	l.buf.Write(uriJSON)
	l.buf.WriteString(",\n  \"created\": ")
	l.buf.Write(createdJSON)
	l.buf.WriteString(",\n  \"files\": [")

	return l, nil
}

func (l *fileListWriter) add(f fileListFile) error {
	entry, err := json.MarshalIndent(f, "    ", "  ")
	if err != nil {
		return err
	}

	if l.entries > 0 {
		l.buf.WriteByte(',')
	}

	l.buf.WriteString("\n    ")
	l.buf.Write(entry)

	l.entries++
	l.pending++

	if l.pending >= fileListFlushEntries {
		return l.flush()
	}

	return nil
}

func (l *fileListWriter) flush() error {
	if !l.started {
		l.w.Header().Set("Content-Type", "application/json")
		l.w.WriteHeader(http.StatusOK)
		l.started = true
	}

	l.pending = 0

	_, err := l.buf.WriteTo(l.w)

	return err
}

func (l *fileListWriter) close() error {
	if l.entries > 0 {
		l.buf.WriteString("\n  ")
	}

	l.buf.WriteString("]\n}\n")

	return l.flush()
}
