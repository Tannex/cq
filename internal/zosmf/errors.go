package zosmf

import (
	"errors"
	"fmt"
)

// ErrNoProgress identifies an inclusive-cursor response that cannot advance.
var ErrNoProgress = errors.New("z/OSMF pagination made no progress")

// RequestError reports a locally rejected request. It is safe to display and
// never contains session credentials.
type RequestError struct {
	Field   string
	Message string
}

func (e *RequestError) Error() string {
	if e.Field == "" {
		return "invalid z/OSMF request: " + e.Message
	}
	return fmt.Sprintf("invalid z/OSMF request %s: %s", e.Field, e.Message)
}

// HTTPError is a bounded, credential-safe representation of a non-success
// z/OSMF response.
type HTTPError struct {
	StatusCode int
	Resource   string
	Code       string
	Message    string
	Truncated  bool
}

func (e *HTTPError) Error() string {
	message := e.Message
	if e.Code != "" {
		if message != "" {
			message = e.Code + ": " + message
		} else {
			message = e.Code
		}
	}
	if e.Truncated {
		message += " (error response truncated)"
	}
	if e.Resource == "" {
		return fmt.Sprintf("z/OSMF %d: %s", e.StatusCode, message)
	}
	return fmt.Sprintf("z/OSMF %d for %s: %s", e.StatusCode, e.Resource, message)
}

// ProtocolError reports a malformed successful response without exposing raw
// response bytes or request credentials.
type ProtocolError struct {
	Operation string
	Message   string
}

func (e *ProtocolError) Error() string {
	if e.Operation == "" {
		return "invalid z/OSMF response: " + e.Message
	}
	return fmt.Sprintf("invalid z/OSMF %s response: %s", e.Operation, e.Message)
}

// LimitError reports that a bounded response exceeded a local safety limit.
type LimitError struct {
	Kind  string
	Limit int64
}

func (e *LimitError) Error() string {
	return fmt.Sprintf("z/OSMF %s exceeds the safe limit of %d bytes", e.Kind, e.Limit)
}

// NoProgressError reports the cursor which failed to advance.
type NoProgressError struct {
	Resource string
	Start    string
}

func (e *NoProgressError) Error() string {
	return fmt.Sprintf("%s for %s at inclusive start %q", ErrNoProgress, e.Resource, e.Start)
}

func (e *NoProgressError) Unwrap() error { return ErrNoProgress }
