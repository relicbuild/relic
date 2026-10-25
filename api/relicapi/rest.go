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

var restHTTPStatuses = map[connect.Code]int{
	connect.CodeCanceled:           499,
	connect.CodeInvalidArgument:    http.StatusBadRequest,
	connect.CodeDeadlineExceeded:   http.StatusGatewayTimeout,
	connect.CodeNotFound:           http.StatusNotFound,
	connect.CodeAlreadyExists:      http.StatusConflict,
	connect.CodePermissionDenied:   http.StatusForbidden,
	connect.CodeResourceExhausted:  http.StatusTooManyRequests,
	connect.CodeFailedPrecondition: http.StatusBadRequest,
	connect.CodeAborted:            http.StatusConflict,
	connect.CodeOutOfRange:         http.StatusBadRequest,
	connect.CodeUnimplemented:      http.StatusNotImplemented,
	connect.CodeUnavailable:        http.StatusServiceUnavailable,
	connect.CodeUnauthenticated:    http.StatusUnauthorized,
}

func restHTTPStatus(code connect.Code) int {
	if status, ok := restHTTPStatuses[code]; ok {
		return status
	}

	return http.StatusInternalServerError
}

func writeRESTError(w http.ResponseWriter, status int, ce *connect.Error) {
	body, err := json.Marshal(restStatus{
		Code:    int(ce.Code()),
		Message: ce.Message(),
		Details: []json.RawMessage{},
	})
	if err != nil {
		status, body = http.StatusInternalServerError, []byte(`{"code":13,"message":"internal error","details":[]}`)
	}

	if status >= http.StatusInternalServerError {
		w.Header().Set("Cache-Control", "no-store")
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
