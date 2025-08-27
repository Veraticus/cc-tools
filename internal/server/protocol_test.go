package server

import (
	"encoding/json"
	"testing"
)

func TestRequestID_MarshalJSON(t *testing.T) {
	tests := []struct {
		name     string
		id       RequestID
		expected string
	}{
		{
			name:     "string ID",
			id:       RequestID{value: "test-id"},
			expected: `"test-id"`,
		},
		{
			name:     "numeric string ID",
			id:       RequestID{value: "123"},
			expected: `"123"`,
		},
		{
			name:     "empty ID",
			id:       RequestID{value: ""},
			expected: `""`,
		},
		{
			name:     "UUID-like ID",
			id:       RequestID{value: "550e8400-e29b-41d4-a716-446655440000"},
			expected: `"550e8400-e29b-41d4-a716-446655440000"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.id)
			if err != nil {
				t.Fatalf("Failed to marshal RequestID: %v", err)
			}

			if string(data) != tt.expected {
				t.Errorf("Expected %s, got %s", tt.expected, string(data))
			}
		})
	}
}

func TestRequestID_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expected    string
		shouldError bool
	}{
		{
			name:     "string ID",
			input:    `"test-id"`,
			expected: "test-id",
		},
		{
			name:     "numeric ID",
			input:    `123`,
			expected: "123",
		},
		{
			name:     "null ID",
			input:    `null`,
			expected: "",
		},
		{
			name:     "float ID",
			input:    `123.456`,
			expected: "123.456",
		},
		{
			name:        "invalid JSON",
			input:       `{invalid}`,
			shouldError: true,
		},
		{
			name:        "array ID",
			input:       `["invalid"]`,
			shouldError: true,
		},
		{
			name:        "object ID",
			input:       `{"id": "invalid"}`,
			shouldError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var id RequestID
			err := json.Unmarshal([]byte(tt.input), &id)

			if tt.shouldError {
				if err == nil {
					t.Error("Expected error, got nil")
				}
			} else {
				if err != nil {
					t.Fatalf("Unexpected error: %v", err)
				}
				if id.value != tt.expected {
					t.Errorf("Expected value %q, got %q", tt.expected, id.value)
				}
			}
		})
	}
}

func TestNewErrorResponse(t *testing.T) {
	tests := []struct {
		name    string
		id      RequestID
		code    int
		message string
	}{
		{
			name:    "parse error",
			id:      RequestID{value: "1"},
			code:    ParseError,
			message: "Parse error",
		},
		{
			name:    "invalid request",
			id:      RequestID{value: "2"},
			code:    InvalidRequest,
			message: "Invalid Request",
		},
		{
			name:    "method not found",
			id:      RequestID{value: "3"},
			code:    MethodNotFound,
			message: "Method not found",
		},
		{
			name:    "invalid params",
			id:      RequestID{value: "4"},
			code:    InvalidParams,
			message: "Invalid params",
		},
		{
			name:    "internal error",
			id:      RequestID{value: "5"},
			code:    InternalError,
			message: "Internal error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := NewErrorResponse(tt.id, tt.code, tt.message)

			if resp.JSONRPC != "2.0" {
				t.Errorf("Expected JSONRPC version 2.0, got %s", resp.JSONRPC)
			}

			if resp.ID.value != tt.id.value {
				t.Errorf("Expected ID %s, got %s", tt.id.value, resp.ID.value)
			}

			if resp.Error == nil {
				t.Fatal("Expected error in response, got nil")
			}

			if resp.Error.Code != tt.code {
				t.Errorf("Expected error code %d, got %d", tt.code, resp.Error.Code)
			}

			if resp.Error.Message != tt.message {
				t.Errorf("Expected error message %q, got %q", tt.message, resp.Error.Message)
			}

			if resp.Result != nil {
				t.Error("Expected nil result in error response")
			}
		})
	}
}

func TestNewSuccessResponse(t *testing.T) {
	tests := []struct {
		name   string
		id     RequestID
		output string
	}{
		{
			name:   "simple output",
			id:     RequestID{value: "1"},
			output: "success",
		},
		{
			name:   "JSON output",
			id:     RequestID{value: "2"},
			output: `{"result": "ok", "data": [1, 2, 3]}`,
		},
		{
			name:   "empty output",
			id:     RequestID{value: "3"},
			output: "",
		},
		{
			name:   "multiline output",
			id:     RequestID{value: "4"},
			output: "line1\nline2\nline3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := NewSuccessResponse(tt.id, tt.output)

			if resp.JSONRPC != "2.0" {
				t.Errorf("Expected JSONRPC version 2.0, got %s", resp.JSONRPC)
			}

			if resp.ID.value != tt.id.value {
				t.Errorf("Expected ID %s, got %s", tt.id.value, resp.ID.value)
			}

			if resp.Error != nil {
				t.Errorf("Expected nil error in success response, got %v", resp.Error)
			}

			if resp.Result == nil {
				t.Fatal("Expected result in success response, got nil")
			}

			if resp.Result.Output != tt.output {
				t.Errorf("Expected output %q, got %q", tt.output, resp.Result.Output)
			}
		})
	}
}

func TestRequest_Serialization(t *testing.T) {
	req := Request{
		JSONRPC: "2.0",
		ID:      RequestID{value: "test-123"},
		Method:  "lint",
		Params:  json.RawMessage(`{"input": "test code", "project": "myproject"}`),
	}

	// Marshal
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Failed to marshal request: %v", err)
	}

	// Unmarshal
	var decoded Request
	if unmarshalErr := json.Unmarshal(data, &decoded); unmarshalErr != nil {
		t.Fatalf("Failed to unmarshal request: %v", unmarshalErr)
	}

	// Verify fields
	if decoded.JSONRPC != req.JSONRPC {
		t.Errorf("JSONRPC mismatch: expected %s, got %s", req.JSONRPC, decoded.JSONRPC)
	}

	if decoded.ID.value != req.ID.value {
		t.Errorf("ID mismatch: expected %s, got %s", req.ID.value, decoded.ID.value)
	}

	if decoded.Method != req.Method {
		t.Errorf("Method mismatch: expected %s, got %s", req.Method, decoded.Method)
	}

	// Compare params as parsed JSON to ignore formatting differences
	var expectedParams, decodedParams map[string]any
	json.Unmarshal(req.Params, &expectedParams)
	json.Unmarshal(decoded.Params, &decodedParams)

	if len(expectedParams) != len(decodedParams) {
		t.Errorf("Params mismatch: different number of keys")
	}
	for k, v := range expectedParams {
		if decodedParams[k] != v {
			t.Errorf("Params mismatch for key %s: expected %v, got %v", k, v, decodedParams[k])
		}
	}
}

func TestResponse_Serialization(t *testing.T) {
	tests := []struct {
		name     string
		response Response
	}{
		{
			name: "success response",
			response: Response{
				JSONRPC: "2.0",
				ID:      RequestID{value: "test-123"},
				Result:  &Result{Output: "success"},
			},
		},
		{
			name: "error response",
			response: Response{
				JSONRPC: "2.0",
				ID:      RequestID{value: "test-456"},
				Error: &Error{
					Code:    InvalidParams,
					Message: "Invalid parameters",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Marshal
			data, err := json.Marshal(tt.response)
			if err != nil {
				t.Fatalf("Failed to marshal response: %v", err)
			}

			// Unmarshal
			var decoded Response
			if unmarshalErr := json.Unmarshal(data, &decoded); unmarshalErr != nil {
				t.Fatalf("Failed to unmarshal response: %v", unmarshalErr)
			}

			// Verify fields
			if decoded.JSONRPC != tt.response.JSONRPC {
				t.Errorf("JSONRPC mismatch: expected %s, got %s", tt.response.JSONRPC, decoded.JSONRPC)
			}

			if decoded.ID.value != tt.response.ID.value {
				t.Errorf("ID mismatch: expected %s, got %s", tt.response.ID.value, decoded.ID.value)
			}

			if tt.response.Result != nil {
				if decoded.Result == nil {
					t.Error("Expected result, got nil")
				} else if decoded.Result.Output != tt.response.Result.Output {
					t.Errorf("Result mismatch: expected %s, got %s",
						tt.response.Result.Output, decoded.Result.Output)
				}
			}

			if tt.response.Error != nil {
				if decoded.Error == nil {
					t.Error("Expected error, got nil")
					return
				}
				if decoded.Error.Code != tt.response.Error.Code {
					t.Errorf("Error code mismatch: expected %d, got %d",
						tt.response.Error.Code, decoded.Error.Code)
				}
				if decoded.Error.Message != tt.response.Error.Message {
					t.Errorf("Error message mismatch: expected %s, got %s",
						tt.response.Error.Message, decoded.Error.Message)
				}
			}
		})
	}
}

func TestMethodParams_Serialization(t *testing.T) {
	tests := []struct {
		name   string
		params MethodParams
	}{
		{
			name: "full params",
			params: MethodParams{
				Input:   "test code",
				Project: "myproject",
				Timeout: 5000,
			},
		},
		{
			name: "minimal params",
			params: MethodParams{
				Input: "test",
			},
		},
		{
			name:   "empty params",
			params: MethodParams{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Marshal
			data, err := json.Marshal(tt.params)
			if err != nil {
				t.Fatalf("Failed to marshal params: %v", err)
			}

			// Unmarshal
			var decoded MethodParams
			if unmarshalErr := json.Unmarshal(data, &decoded); unmarshalErr != nil {
				t.Fatalf("Failed to unmarshal params: %v", unmarshalErr)
			}

			// Verify fields
			if decoded.Input != tt.params.Input {
				t.Errorf("Input mismatch: expected %q, got %q", tt.params.Input, decoded.Input)
			}

			if decoded.Project != tt.params.Project {
				t.Errorf("Project mismatch: expected %q, got %q", tt.params.Project, decoded.Project)
			}

			if decoded.Timeout != tt.params.Timeout {
				t.Errorf("Timeout mismatch: expected %d, got %d", tt.params.Timeout, decoded.Timeout)
			}
		})
	}
}
