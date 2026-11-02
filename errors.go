package slidecold

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// SettingsURL is where API keys are created.
const SettingsURL = "https://slidecold.com/settings"

// ErrNoAPIKey is returned when no API key was configured.
var ErrNoAPIKey = errors.New("slidecold: no API key: set SLIDECOLD_API_KEY, run `slidecold login`, or pass WithAPIKey (create a key at " + SettingsURL + ")")

// Error is a non-2xx API response.
type Error struct {
	StatusCode int
	// Message is the "error" field of the response body, or the HTTP status text.
	Message string
	// Body is the raw response body.
	Body []byte
}

func newError(r *Response) *Error {
	e := &Error{StatusCode: r.StatusCode, Body: r.Body}
	var b struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(r.Body, &b) == nil && b.Error != "" {
		e.Message = b.Error
	} else {
		e.Message = http.StatusText(r.StatusCode)
	}
	return e
}

func (e *Error) Error() string {
	msg := fmt.Sprintf("slidecold: %d %s", e.StatusCode, e.Message)
	if e.StatusCode == http.StatusUnauthorized {
		msg += " (check your API key, or create one at " + SettingsURL + ")"
	}
	return msg
}

func statusIs(err error, code int) bool {
	var e *Error
	return errors.As(err, &e) && e.StatusCode == code
}

// IsAuthError reports a missing or invalid API key (401, or ErrNoAPIKey).
func IsAuthError(err error) bool {
	return errors.Is(err, ErrNoAPIKey) || statusIs(err, http.StatusUnauthorized)
}

// IsPermissionError reports a 403: the key lacks a scope, or a plan limit was hit.
func IsPermissionError(err error) bool { return statusIs(err, http.StatusForbidden) }

// IsNotFound reports a 404.
func IsNotFound(err error) bool { return statusIs(err, http.StatusNotFound) }

// IsRateLimited reports a 429.
func IsRateLimited(err error) bool { return statusIs(err, http.StatusTooManyRequests) }
