// Package httperror writes the errors the HTTP layer answers before a handler
// runs.
package httperror

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
)

// RelicAPI reports whether path belongs to the Relic API.
func RelicAPI(path string) bool {
	return strings.HasPrefix(path, "/api/relic.") || strings.HasPrefix(path, "/api/v1/")
}

// Writer writes an error in one API's shape.
type Writer func(w http.ResponseWriter, status int, message string)

var writers = struct {
	sync.RWMutex
	byPrefix map[string]Writer
}{
	byPrefix: map[string]Writer{},
}

// Register makes Write answer requests under prefix with writer.
func Register(prefix string, writer Writer) {
	writers.Lock()
	defer writers.Unlock()

	writers.byPrefix[prefix] = writer
}

func writerFor(path string) Writer {
	writers.RLock()
	defer writers.RUnlock()

	var (
		best  string
		found Writer
	)

	for prefix, w := range writers.byPrefix {
		if strings.HasPrefix(path, prefix) && len(prefix) > len(best) {
			best, found = prefix, w
		}
	}

	return found
}

var connectCodes = map[int]string{
	http.StatusBadRequest:                   "invalid_argument",
	http.StatusUnauthorized:                 "unauthenticated",
	http.StatusForbidden:                    "permission_denied",
	http.StatusNotFound:                     "not_found",
	http.StatusMethodNotAllowed:             "unimplemented",
	http.StatusConflict:                     "already_exists",
	http.StatusUnprocessableEntity:          "invalid_argument",
	http.StatusRequestEntityTooLarge:        "out_of_range",
	http.StatusRequestedRangeNotSatisfiable: "out_of_range",
	http.StatusTooManyRequests:              "resource_exhausted",
	http.StatusServiceUnavailable:           "unavailable",
}

// Write answers r with an error.
func Write(w http.ResponseWriter, r *http.Request, status int, message string) {
	if status >= http.StatusInternalServerError {
		w.Header().Set("Cache-Control", "no-store")
	}

	if r != nil && !RelicAPI(r.URL.Path) {
		if writer := writerFor(r.URL.Path); writer != nil {
			writer(w, status, message)
			return
		}
	}

	c, ok := connectCodes[status]
	if !ok {
		c = "internal"
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(map[string]string{
		"code":    c,
		"message": message,
	})
}
