package reikemmail

import (
	"context"
	"errors"
	"fmt"
)

// TransportError reports a failed HTTP exchange. Its message omits request
// details and credentials. Use errors.Is for context cancellation and timeouts.
type TransportError struct {
	cause error
}

// Error describes the transport failure without exposing request details.
func (e *TransportError) Error() string { return "mail API transport failed or timed out" }

// Unwrap returns context.Canceled or context.DeadlineExceeded when applicable.
func (e *TransportError) Unwrap() error { return e.cause }

func newTransportError(err error) *TransportError {
	var timeout interface{ Timeout() bool }
	switch {
	case errors.Is(err, context.Canceled):
		return &TransportError{cause: context.Canceled}
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &timeout) && timeout.Timeout():
		return &TransportError{cause: context.DeadlineExceeded}
	default:
		return &TransportError{}
	}
}

// ResponseError reports a response that is unusable despite completing the HTTP exchange.
type ResponseError struct {
	HTTPCode int
	reason   string
}

// Error describes the response failure without exposing its body.
func (e *ResponseError) Error() string {
	return fmt.Sprintf("mail API returned %s (HTTP %d)", e.reason, e.HTTPCode)
}
