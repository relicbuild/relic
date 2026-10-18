package relicapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"connectrpc.com/connect/v2"
)

// restDecodeErrors answers 400 invalid_argument where the transcoder reports
// a REST request it cannot decode as unknown.
func restDecodeErrors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, Prefix+"relic.v1.") {
			next.ServeHTTP(w, r)

			return
		}

		errorWriter := &restErrorWriter{
			ResponseWriter: w,
		}
		next.ServeHTTP(errorWriter, r)
		errorWriter.finish()
	})
}

type restStatus struct {
	Code    int               `json:"code"`
	Message string            `json:"message"`
	Details []json.RawMessage `json:"details"`
}

type restErrorWriter struct {
	http.ResponseWriter

	held          bool
	headerWritten bool
	status        int
	body          bytes.Buffer
}

func (w *restErrorWriter) WriteHeader(status int) {
	if w.headerWritten || w.held {
		return
	}

	if status >= http.StatusInternalServerError {
		w.held = true
		w.status = status

		return
	}

	w.headerWritten = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *restErrorWriter) Write(p []byte) (int, error) {
	if w.held {
		return w.body.Write(p)
	}

	w.headerWritten = true

	return w.ResponseWriter.Write(p)
}

func (w *restErrorWriter) Flush() {
	if w.held {
		return
	}

	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		w.headerWritten = true

		f.Flush()
	}
}

func (w *restErrorWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *restErrorWriter) finish() {
	if !w.held {
		return
	}

	status, body := w.status, w.body.Bytes()

	var decoded restStatus
	if json.Unmarshal(body, &decoded) == nil && decoded.Code == int(connect.CodeUnknown) {
		decoded.Code = int(connect.CodeInvalidArgument)
		if decoded.Details == nil {
			decoded.Details = []json.RawMessage{}
		}

		if rewritten, err := json.Marshal(decoded); err == nil {
			status, body = http.StatusBadRequest, rewritten
		}
	}

	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.ResponseWriter.WriteHeader(status)
	_, _ = w.ResponseWriter.Write(body)
}
