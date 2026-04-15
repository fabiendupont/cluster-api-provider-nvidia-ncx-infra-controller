/*
Copyright 2026 Fabien Dupont.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package scope

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestClassifyAPIError(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		err        error
		wantType   APIErrorType
		wantNil    bool
	}{
		{
			name:       "200 OK returns nil",
			statusCode: 200,
			err:        nil,
			wantNil:    true,
		},
		{
			name:       "201 Created returns nil",
			statusCode: 201,
			err:        nil,
			wantNil:    true,
		},
		{
			name:       "429 Too Many Requests is transient",
			statusCode: 429,
			err:        fmt.Errorf("rate limited"),
			wantType:   APIErrorTransient,
		},
		{
			name:       "503 Service Unavailable is transient",
			statusCode: 503,
			err:        fmt.Errorf("service unavailable"),
			wantType:   APIErrorTransient,
		},
		{
			name:       "502 Bad Gateway is transient",
			statusCode: 502,
			err:        fmt.Errorf("bad gateway"),
			wantType:   APIErrorTransient,
		},
		{
			name:       "504 Gateway Timeout is transient",
			statusCode: 504,
			err:        fmt.Errorf("timeout"),
			wantType:   APIErrorTransient,
		},
		{
			name:       "409 Conflict is transient",
			statusCode: 409,
			err:        fmt.Errorf("conflict"),
			wantType:   APIErrorTransient,
		},
		{
			name:       "404 Not Found",
			statusCode: 404,
			err:        fmt.Errorf("not found"),
			wantType:   APIErrorNotFound,
		},
		{
			name:       "400 Bad Request is terminal",
			statusCode: 400,
			err:        fmt.Errorf("bad request"),
			wantType:   APIErrorTerminal,
		},
		{
			name:       "500 Internal Server Error is transient",
			statusCode: 500,
			err:        fmt.Errorf("internal error"),
			wantType:   APIErrorTransient,
		},
		{
			name:       "nil response with error is transient",
			statusCode: 0,
			err:        fmt.Errorf("connection refused"),
			wantType:   APIErrorTransient,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var httpResp *http.Response
			if tt.statusCode > 0 {
				httpResp = &http.Response{StatusCode: tt.statusCode}
			}
			result := ClassifyAPIError(httpResp, tt.err, "TestMethod")
			if tt.wantNil {
				if result != nil {
					t.Errorf("expected nil, got %v", result)
				}
				return
			}
			if result == nil {
				t.Fatal("expected non-nil APIError")
			}
			if result.Type != tt.wantType {
				t.Errorf("expected type %v, got %v", tt.wantType, result.Type)
			}
		})
	}
}

func TestAPIErrorMethods(t *testing.T) {
	transient := &APIError{Type: APIErrorTransient, Message: "transient"}
	terminal := &APIError{Type: APIErrorTerminal, Message: "terminal"}
	notFound := &APIError{Type: APIErrorNotFound, Message: "not found"}

	if !transient.IsTransient() {
		t.Error("transient error should be transient")
	}
	if transient.IsTerminal() {
		t.Error("transient error should not be terminal")
	}
	if !terminal.IsTerminal() {
		t.Error("terminal error should be terminal")
	}
	if !notFound.IsNotFound() {
		t.Error("not found error should be not found")
	}
}

func TestRequeueAfterForAttempt(t *testing.T) {
	tests := []struct {
		attempt  int
		expected time.Duration
	}{
		{0, 1 * time.Second},
		{1, 2 * time.Second},
		{2, 4 * time.Second},
		{3, 8 * time.Second},
		{4, 16 * time.Second},
		{5, 32 * time.Second},
		{6, 60 * time.Second}, // capped at 60s
		{10, 60 * time.Second},
	}

	for _, tt := range tests {
		result := RequeueAfterForAttempt(tt.attempt)
		if result != tt.expected {
			t.Errorf("attempt %d: expected %v, got %v", tt.attempt, tt.expected, result)
		}
	}
}
