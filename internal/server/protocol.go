package server

import "encoding/json"

// RequestID represents a JSON-RPC request ID (can be string or number).
type RequestID struct {
	value string
}

// MarshalJSON implements json.Marshaler.
func (id RequestID) MarshalJSON() ([]byte, error) {
	return json.Marshal(id.value)
}

// UnmarshalJSON implements json.Unmarshaler.
func (id *RequestID) UnmarshalJSON(data []byte) error {
	// Store the raw JSON as string for now
	id.value = string(data)
	return nil
}

// Request represents a JSON-RPC 2.0 request with concrete types.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      RequestID       `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response represents a JSON-RPC 2.0 response with concrete types.
type Response struct {
	JSONRPC string     `json:"jsonrpc"`
	ID      RequestID  `json:"id"`
	Result  *Result    `json:"result,omitempty"`
	Error   *Error     `json:"error,omitempty"`
}

// Result represents a successful response.
type Result struct {
	Output string            `json:"output"`
	Meta   map[string]string `json:"meta,omitempty"`
}

// Error represents a JSON-RPC 2.0 error.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data,omitempty"`
}

// Standard JSON-RPC 2.0 error codes.
const (
	ParseError     = -32700
	InvalidRequest = -32600
	MethodNotFound = -32601
	InvalidParams  = -32602
	InternalError  = -32603
)

// MethodParams contains parameters for method calls.
type MethodParams struct {
	Input   string `json:"input"`
	Project string `json:"project,omitempty"`
	Timeout int    `json:"timeout,omitempty"` // seconds
}

// NewErrorResponse creates an error response.
func NewErrorResponse(id RequestID, code int, message string) Response {
	return Response{
		JSONRPC: "2.0",
		ID:      id,
		Error: &Error{
			Code:    code,
			Message: message,
		},
	}
}

// NewSuccessResponse creates a success response.
func NewSuccessResponse(id RequestID, output string) Response {
	return Response{
		JSONRPC: "2.0",
		ID:      id,
		Result: &Result{
			Output: output,
		},
	}
}

// NewSuccessResponseWithMeta creates a success response with metadata.
func NewSuccessResponseWithMeta(id RequestID, output string, meta map[string]string) Response {
	return Response{
		JSONRPC: "2.0",
		ID:      id,
		Result: &Result{
			Output: output,
			Meta:   meta,
		},
	}
}