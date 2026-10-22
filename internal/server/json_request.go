/*
Package server validates bounded, unambiguous JSON request bodies.

The helper preserves explicit body limits and rejects duplicate object keys
before typed decoding so security-sensitive fields cannot be shadowed.
*/
package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/DingZhizCA/homelab-monitor/internal/jsonutil"
)

func decodeSingleJSONBody(w http.ResponseWriter, r *http.Request, maxBytes int64, target any, errorCode, errorMessage, tooLargeCode string) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			if tooLargeCode == "" {
				writeError(w, http.StatusBadRequest, errorCode, errorMessage)
			} else {
				writeError(w, http.StatusRequestEntityTooLarge, tooLargeCode, "request body exceeds the configured limit")
			}
		} else {
			writeError(w, http.StatusBadRequest, errorCode, errorMessage)
		}
		return false
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		writeError(w, http.StatusBadRequest, errorCode, errorMessage)
		return false
	}
	if err := jsonutil.RejectDuplicateObjectKeys(body); err != nil {
		writeError(w, http.StatusBadRequest, errorCode, errorMessage)
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, errorCode, errorMessage)
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, errorCode, errorMessage)
		return false
	}
	return true
}
