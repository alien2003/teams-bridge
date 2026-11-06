package fcm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"
)

// Sentinel errors; every *Error matches exactly one of them with errors.Is (or none, for an
// unclassifiable response). Network failures match ErrUnavailable.
var (
	ErrUnauthenticated = errors.New("fcm: unauthenticated")
	ErrNotFound        = errors.New("fcm: not found")
	ErrQuota           = errors.New("fcm: quota exceeded")
	ErrInvalid         = errors.New("fcm: invalid argument")
	ErrUnavailable     = errors.New("fcm: service unavailable")
	ErrPermission      = errors.New("fcm: permission denied")
)

// Google RPC status names (google.rpc.Code) that FCM and this package use.
const (
	StatusInvalidArgument   = "INVALID_ARGUMENT"
	StatusUnauthenticated   = "UNAUTHENTICATED"
	StatusPermissionDenied  = "PERMISSION_DENIED"
	StatusNotFound          = "NOT_FOUND"
	StatusResourceExhausted = "RESOURCE_EXHAUSTED"
	StatusInternal          = "INTERNAL"
	StatusUnavailable       = "UNAVAILABLE"
	StatusDeadlineExceeded  = "DEADLINE_EXCEEDED"
	StatusUnknown           = "UNKNOWN"
)

// FCM error codes (google.firebase.fcm.v1.FcmError.errorCode).
const (
	CodeUnregistered        = "UNREGISTERED"
	CodeQuotaExceeded       = "QUOTA_EXCEEDED"
	CodeSenderIDMismatch    = "SENDER_ID_MISMATCH"
	CodeThirdPartyAuthError = "THIRD_PARTY_AUTH_ERROR"
	CodeInvalidArgument     = "INVALID_ARGUMENT"
	CodeUnavailable         = "UNAVAILABLE"
	CodeInternal            = "INTERNAL"
)

const (
	opToken = "token"
	opSend  = "send"

	fcmErrorType  = "type.googleapis.com/google.firebase.fcm.v1.FcmError"
	maxMessageLen = 300
	// maxResponse bounds how much of a send response is read (FCM's are well under 1 KiB).
	maxResponse = 64 << 10
)

// Error is a failure reported by the token endpoint or by FCM, or a network failure talking to
// them. It never contains the private key, the assertion or an access token.
type Error struct {
	// Op is "token" (OAuth token endpoint) or "send" (projects.messages.send).
	Op string
	// HTTPStatus is the response status code; 0 for a network failure.
	HTTPStatus int
	// Status is the Google RPC status (UNAUTHENTICATED, NOT_FOUND, RESOURCE_EXHAUSTED,
	// INVALID_ARGUMENT, UNAVAILABLE, INTERNAL, PERMISSION_DENIED, …). Derived from HTTPStatus
	// when the body has none, and from the OAuth error for the token endpoint.
	Status string
	// FCMCode is details[].errorCode of a google.firebase.fcm.v1.FcmError (UNREGISTERED,
	// QUOTA_EXCEEDED, SENDER_ID_MISMATCH, THIRD_PARTY_AUTH_ERROR, INVALID_ARGUMENT, …).
	FCMCode string
	// OAuthError is the token endpoint's "error" (invalid_grant, invalid_client, …).
	OAuthError string
	// Message is the server's explanation (error.message / error_description), shortened.
	Message string
	// Hint is a suggestion for the user, e.g. to check the system clock.
	Hint string
	// RetryAfter is the server's Retry-After, if any.
	RetryAfter time.Duration
	// Retryable reports whether sending the same request again later may succeed
	// (429, 5xx, network failures).
	Retryable bool

	cause error
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("fcm: ")
	b.WriteString(e.Op)
	if e.HTTPStatus != 0 {
		fmt.Fprintf(&b, ": HTTP %d", e.HTTPStatus)
	} else if e.cause != nil {
		b.WriteString(": network error")
	}
	if e.Status != "" {
		b.WriteString(" " + e.Status)
	}
	if e.FCMCode != "" && e.FCMCode != e.Status {
		b.WriteString("/" + e.FCMCode)
	}
	if e.OAuthError != "" {
		b.WriteString(" " + e.OAuthError)
	}
	if e.Message != "" {
		b.WriteString(": " + e.Message)
	}
	if e.cause != nil {
		b.WriteString(": " + e.cause.Error())
	}
	if e.RetryAfter > 0 {
		fmt.Fprintf(&b, " (retry after %s)", e.RetryAfter)
	}
	if e.Hint != "" {
		b.WriteString(" — " + e.Hint)
	}
	return b.String()
}

// Unwrap returns the network error, if any.
func (e *Error) Unwrap() error { return e.cause }

// Is matches the sentinel for e's class.
func (e *Error) Is(target error) bool {
	s := e.sentinel()
	return s != nil && s == target
}

func (e *Error) sentinel() error {
	switch e.Status {
	case StatusUnauthenticated:
		return ErrUnauthenticated
	case StatusPermissionDenied:
		return ErrPermission
	case StatusNotFound:
		return ErrNotFound
	case StatusResourceExhausted:
		return ErrQuota
	case StatusInvalidArgument, "FAILED_PRECONDITION", "OUT_OF_RANGE":
		return ErrInvalid
	case StatusUnavailable, StatusInternal, StatusDeadlineExceeded, "ABORTED":
		return ErrUnavailable
	}
	return nil
}

// statusForHTTP is the canonical RPC status for an HTTP status (google.rpc.Code mapping).
func statusForHTTP(code int) string {
	switch {
	case code == http.StatusBadRequest:
		return StatusInvalidArgument
	case code == http.StatusUnauthorized:
		return StatusUnauthenticated
	case code == http.StatusForbidden:
		return StatusPermissionDenied
	case code == http.StatusNotFound:
		return StatusNotFound
	case code == http.StatusTooManyRequests:
		return StatusResourceExhausted
	case code == http.StatusGatewayTimeout:
		return StatusDeadlineExceeded
	case code == http.StatusInternalServerError:
		return StatusInternal
	case code >= 500:
		return StatusUnavailable
	case code >= 400:
		return StatusInvalidArgument
	}
	return StatusUnknown
}

func retryableHTTP(code int) bool {
	return code == http.StatusTooManyRequests || (code >= 500 && code != http.StatusNotImplemented)
}

// googleError is the error body of Google APIs.
type googleError struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
		Details []struct {
			Type      string `json:"@type"`
			ErrorCode string `json:"errorCode"`
		} `json:"details"`
	} `json:"error"`
}

// sendError maps a non-2xx FCM response.
func sendError(resp *http.Response, body []byte, now time.Time) *Error {
	e := &Error{Op: opSend, HTTPStatus: resp.StatusCode, Retryable: retryableHTTP(resp.StatusCode),
		RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), now)}
	var ge googleError
	if json.Unmarshal(body, &ge) == nil {
		e.Status = rpcStatus(ge.Error.Status)
		e.Message = clean(ge.Error.Message)
		for _, d := range ge.Error.Details {
			if d.Type == fcmErrorType && d.ErrorCode != "" {
				e.FCMCode = rpcStatus(d.ErrorCode)
				break
			}
		}
	} else if len(body) > 0 {
		e.Message = "non-JSON response"
	}
	if e.Status == "" {
		e.Status = statusForHTTP(resp.StatusCode)
	}
	switch {
	case resp.StatusCode == http.StatusForbidden && e.FCMCode == "":
		e.Hint = "is the Firebase Cloud Messaging API enabled, and does the service account belong to this project?"
	case resp.StatusCode == http.StatusNotFound && e.FCMCode == "":
		e.Hint = "check project_id in the service-account file"
	}
	return e
}

// tokenError maps a non-200 response of the OAuth token endpoint ({"error","error_description"}).
func tokenError(resp *http.Response, body []byte, now time.Time) *Error {
	e := &Error{Op: opToken, HTTPStatus: resp.StatusCode, Retryable: retryableHTTP(resp.StatusCode),
		RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), now)}
	var oe struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if json.Unmarshal(body, &oe) == nil {
		e.OAuthError = clean(oe.Error)
		e.Message = clean(oe.Description)
	} else if len(body) > 0 {
		e.Message = "non-JSON response"
	}
	switch {
	case e.Retryable:
		e.Status = statusForHTTP(resp.StatusCode)
	case e.OAuthError == "invalid_grant" || e.OAuthError == "invalid_client" ||
		e.OAuthError == "unauthorized_client" || e.OAuthError == "deleted_client" || e.OAuthError == "disabled_client":
		e.Status = StatusUnauthenticated
	case e.OAuthError == "access_denied" || e.OAuthError == "admin_policy_enforced" || e.OAuthError == "org_internal":
		e.Status = StatusPermissionDenied
	case e.OAuthError == "invalid_scope" || e.OAuthError == "invalid_request" || e.OAuthError == "unsupported_grant_type":
		e.Status = StatusInvalidArgument
	default:
		e.Status = statusForHTTP(resp.StatusCode)
	}
	lower := strings.ToLower(e.Message)
	switch {
	case strings.Contains(lower, "timeframe") || strings.Contains(lower, "short-lived") ||
		strings.Contains(lower, "iat and exp") || strings.Contains(lower, "used too early") ||
		strings.Contains(lower, "used too late"):
		e.Hint = "check the system clock: the JWT's iat/exp are taken from it (WSL clocks drift after the host sleeps)"
	case strings.Contains(lower, "signature") || strings.Contains(lower, "account not found") ||
		e.OAuthError == "invalid_client" || e.OAuthError == "disabled_client" || e.OAuthError == "deleted_client":
		e.Hint = "the service-account key was deleted, disabled or belongs to another account; create a new key in the Firebase console"
	case resp.StatusCode == http.StatusNotFound:
		e.Hint = "check token_uri in the service-account file"
	}
	return e
}

// transportError wraps a failure to talk to the server. Cancellation by the caller is returned as
// is (it matches context.Canceled / context.DeadlineExceeded), never as a retryable *Error.
func transportError(ctx context.Context, op string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("fcm: %s: %w", op, ctxErr)
	}
	return &Error{Op: op, Status: StatusUnavailable, Retryable: true, cause: err}
}

func readBody(resp *http.Response, limit int64) ([]byte, error) {
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	// Drain a little more so the connection can be reused, then give up on it.
	_, _ = io.CopyN(io.Discard, resp.Body, 4<<10)
	return b, err
}

// rpcStatus keeps only well-formed status/code names (UPPER_SNAKE).
func rpcStatus(s string) string {
	if len(s) == 0 || len(s) > 40 {
		return ""
	}
	for _, r := range s {
		if !(r >= 'A' && r <= 'Z' || r == '_' || r >= '0' && r <= '9') {
			return ""
		}
	}
	return s
}

// clean shortens a server message and strips control characters, so a hostile or broken response
// cannot flood or forge log lines.
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.TrimSpace(s))
	if len(s) > maxMessageLen {
		cut := maxMessageLen
		for cut > 0 && !isRuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + "…"
	}
	return s
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
