// Package wire holds the JSON helpers shared by the adapter's APIs.
package wire

import (
	"encoding/json"
	"errors"
	"net/http"
)

// ErrorBody is the adapter's REST error envelope.
type ErrorBody struct {
	Errors []ErrorEntry `json:"errors"`
}

// ErrorEntry is one error in an ErrorBody.
type ErrorEntry struct {
	Status  int    `json:"status"`
	Message string `json:"message"`
}

// WriteError writes a JSON error response.
func WriteError(w http.ResponseWriter, status int, message string) {
	WriteJSON(w, status, ErrorBody{
		Errors: []ErrorEntry{
			{
				Status:  status,
				Message: message,
			},
		},
	})
}

// WriteBodyError answers a request body that could not be read: 413 with
// tooLarge when it passed its size limit, 400 otherwise.
func WriteBodyError(w http.ResponseWriter, err error, tooLarge string) {
	var limit *http.MaxBytesError
	if errors.As(err, &limit) {
		WriteError(w, http.StatusRequestEntityTooLarge, tooLarge)
		return
	}

	WriteError(w, http.StatusBadRequest, "the request body could not be read")
}

// WriteJSON writes v as a JSON response with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}
