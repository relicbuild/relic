package relicapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"connectrpc.com/connect/v2"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
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

type multiSegmentRoute struct {
	method string
	prefix []string
}

func multiSegmentRoutes() []multiSegmentRoute {
	var routes []multiSegmentRoute

	protoregistry.GlobalFiles.RangeFilesByPackage("relic.v1", func(file protoreflect.FileDescriptor) bool {
		services := file.Services()
		for i := range services.Len() {
			methods := services.Get(i).Methods()
			for j := range methods.Len() {
				rule, ok := proto.GetExtension(methods.Get(j).Options(), annotations.E_Http).(*annotations.HttpRule)
				if !ok || rule == nil {
					continue
				}

				bindings := append([]*annotations.HttpRule{
					rule,
				}, rule.GetAdditionalBindings()...)
				for _, binding := range bindings {
					if route, ok := multiSegmentRouteOf(binding); ok {
						routes = append(routes, route)
					}
				}
			}
		}

		return true
	})

	return routes
}

func multiSegmentRouteOf(rule *annotations.HttpRule) (multiSegmentRoute, bool) {
	method, template := httpRulePattern(rule)

	var prefix []string

	for segment := range strings.SplitSeq(strings.TrimPrefix(template, "/"), "/") {
		switch {
		case strings.HasPrefix(segment, "{") && strings.Contains(segment, "=**}"):
			return multiSegmentRoute{
				method: method,
				prefix: prefix,
			}, true
		case strings.HasPrefix(segment, "{"):
			prefix = append(prefix, "")
		default:
			prefix = append(prefix, segment)
		}
	}

	return multiSegmentRoute{}, false
}

func httpRulePattern(rule *annotations.HttpRule) (method, template string) {
	switch pattern := rule.GetPattern().(type) {
	case *annotations.HttpRule_Get:
		return http.MethodGet, pattern.Get
	case *annotations.HttpRule_Put:
		return http.MethodPut, pattern.Put
	case *annotations.HttpRule_Post:
		return http.MethodPost, pattern.Post
	case *annotations.HttpRule_Delete:
		return http.MethodDelete, pattern.Delete
	case *annotations.HttpRule_Patch:
		return http.MethodPatch, pattern.Patch
	case *annotations.HttpRule_Custom:
		return pattern.Custom.GetKind(), pattern.Custom.GetPath()
	}

	return "", ""
}

func (route multiSegmentRoute) matches(method string, segments []string) bool {
	if method != route.method || len(segments) <= len(route.prefix) {
		return false
	}

	for i, literal := range route.prefix {
		if literal == "" && segments[i] == "" || literal != "" && literal != segments[i] {
			return false
		}
	}

	return true
}

func multiSegmentPaths(next http.Handler) http.Handler {
	routes := multiSegmentRoutes()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		escaped := r.URL.EscapedPath()
		if !strings.Contains(strings.ToUpper(escaped), "%2F") {
			next.ServeHTTP(w, r)
			return
		}

		segments := strings.Split(strings.TrimPrefix(escaped, "/"), "/")
		for _, route := range routes {
			if !route.matches(r.Method, segments) {
				continue
			}

			variable := strings.Join(segments[len(route.prefix):], "/")
			variable = strings.NewReplacer("%2F", "/", "%2f", "/").Replace(variable)
			raw := "/" + strings.Join(append(slices.Clone(segments[:len(route.prefix)]), variable), "/")

			decoded, err := url.PathUnescape(raw)
			if err != nil {
				break
			}

			r2 := r.Clone(r.Context())
			r2.URL.Path = decoded
			r2.URL.RawPath = raw
			r = r2

			break
		}

		next.ServeHTTP(w, r)
	})
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
