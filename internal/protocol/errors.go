package protocol

type ErrorType string
type ErrorCode string

const (
	ErrorTypeInvalidRequest ErrorType = "invalid_request_error"
	ErrorTypeGateway        ErrorType = "gateway_error"
)

const (
	ErrorCodeInvalidJSON              ErrorCode = "invalid_json"
	ErrorCodeInvalidValue             ErrorCode = "invalid_value"
	ErrorCodeUnsupportedValue         ErrorCode = "unsupported_value"
	ErrorCodeMissingRequiredParameter ErrorCode = "missing_required_parameter"
	ErrorCodeModelNotFound            ErrorCode = "model_not_found"
	ErrorCodeRequestTooLarge          ErrorCode = "request_too_large"
	ErrorCodeRequestBodyReadFailed    ErrorCode = "request_body_read_failed"
	ErrorCodeUpstreamTimeout          ErrorCode = "upstream_timeout"
	ErrorCodeUpstreamUnavailable      ErrorCode = "upstream_unavailable"
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
