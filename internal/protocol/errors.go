package protocol

import (
	"encoding/json"
	"fmt"
	"net/http"
)

type ErrorType string
type ErrorCode string

const (
	ErrorTypeInvalidRequest ErrorType = "invalid_request_error"
	ErrorTypeAuthentication ErrorType = "authentication_error"
	ErrorTypePermission     ErrorType = "permission_error"
	ErrorTypeRateLimit      ErrorType = "rate_limit_error"
	ErrorTypeServer         ErrorType = "server_error"
)

const (
	ErrorCodeInvalidJSON              ErrorCode = "invalid_json"
	ErrorCodeInvalidValue             ErrorCode = "invalid_value"
	ErrorCodeUnsupportedValue         ErrorCode = "unsupported_value"
	ErrorCodeMissingRequiredParameter ErrorCode = "missing_required_parameter"
	ErrorCodeModelNotFound            ErrorCode = "model_not_found"
	ErrorCodeRequestTooLarge          ErrorCode = "request_too_large"
	ErrorCodeRequestBodyReadFailed    ErrorCode = "request_body_read_failed"
	ErrorCodeRequestEncodingFailed    ErrorCode = "request_encoding_failed"
	ErrorCodeUpstreamTimeout          ErrorCode = "upstream_timeout"
	ErrorCodeUpstreamUnavailable      ErrorCode = "upstream_unavailable"
	ErrorCodeRateLimit                ErrorCode = "rate_limit_exceeded"
	ErrorCodeConcurrencyLimit         ErrorCode = "concurrency_limit_exceeded"
	ErrorCodeInvalidAPIKey            ErrorCode = "invalid_api_key"
	ErrorCodePermissionDenied         ErrorCode = "permission_denied"
	ErrorCodeInternal                 ErrorCode = "internal_error"
	ErrorCodeNotFound                 ErrorCode = "endpoint_not_found"
	ErrorCodeMethodNotAllowed         ErrorCode = "method_not_allowed"
	ErrorCodeUpstreamHTTP             ErrorCode = "upstream_error"
)

type ErrorResponse struct {
	Error APIError `json:"error"`
}

type APIError struct {
	Message string    `json:"message"`
	Type    ErrorType `json:"type"`
	Param   string    `json:"param"`
	Code    ErrorCode `json:"code"`
}

// NewError constructs a client-visible error. Internal diagnostics belong in logs.
func NewError(kind ErrorType, code ErrorCode, message, param string) APIError {
	return APIError{Message: message, Type: kind, Param: param, Code: code}
}

// MarshalJSON emits absent code/param as JSON null, rather than empty strings.
// Keep the service-side value convenient to construct and compare; SDK error
// types are for decoding and also carry HTTP diagnostic fields.
func (e APIError) MarshalJSON() ([]byte, error) {
	var param *string
	var code *ErrorCode
	if e.Param != "" {
		param = &e.Param
	}
	if e.Code != "" {
		code = &e.Code
	}
	return json.Marshal(struct {
		Message string     `json:"message"`
		Type    ErrorType  `json:"type"`
		Param   *string    `json:"param"`
		Code    *ErrorCode `json:"code"`
	}{e.Message, e.Type, param, code})
}

// ErrorForStatus provides a complete fallback for every API error response.
func ErrorForStatus(status int) APIError {
	kind, code := ErrorTypeInvalidRequest, ErrorCodeInvalidValue
	switch {
	case status == http.StatusUnauthorized:
		kind, code = ErrorTypeAuthentication, ErrorCodeInvalidAPIKey
	case status == http.StatusForbidden:
		kind, code = ErrorTypePermission, ErrorCodePermissionDenied
	case status == http.StatusNotFound:
		code = ErrorCodeNotFound
	case status == http.StatusMethodNotAllowed:
		code = ErrorCodeMethodNotAllowed
	case status == http.StatusTooManyRequests:
		kind, code = ErrorTypeRateLimit, ErrorCodeRateLimit
	case status >= 500:
		kind, code = ErrorTypeServer, ErrorCodeInternal
	}
	message := http.StatusText(status)
	if message == "" {
		message = "API request failed."
	}
	return NewError(kind, code, message, "")
}

func UpstreamHTTPError(status int) APIError {
	err := ErrorForStatus(status)
	err.Code = ErrorCodeUpstreamHTTP
	err.Message = fmt.Sprintf("Upstream returned HTTP %d.", status)
	return err
}
