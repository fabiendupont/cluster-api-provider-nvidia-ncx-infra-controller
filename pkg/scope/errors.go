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
	"time"
)

// APIErrorType classifies NICo API errors for retry decisions.
type APIErrorType int

const (
	// APIErrorTransient indicates a retryable error (429, 503, 409, timeout).
	APIErrorTransient APIErrorType = iota
	// APIErrorTerminal indicates a non-retryable error (400 bad request).
	APIErrorTerminal
	// APIErrorNotFound indicates the resource no longer exists (404).
	APIErrorNotFound
)

// APIError wraps an API error with classification and retry metadata.
type APIError struct {
	Type       APIErrorType
	StatusCode int
	Message    string
	Err        error
}

func (e *APIError) Error() string {
	return e.Message
}

func (e *APIError) Unwrap() error {
	return e.Err
}

// IsTransient returns true if the error is retryable.
func (e *APIError) IsTransient() bool {
	return e.Type == APIErrorTransient
}

// IsTerminal returns true if the error is non-retryable.
func (e *APIError) IsTerminal() bool {
	return e.Type == APIErrorTerminal
}

// IsNotFound returns true if the resource was not found.
func (e *APIError) IsNotFound() bool {
	return e.Type == APIErrorNotFound
}

// ClassifyAPIError classifies an HTTP response and error into an APIError.
// Returns nil if the response indicates success (2xx).
func ClassifyAPIError(httpResp *http.Response, err error, method string) *APIError {
	if err == nil && httpResp != nil && httpResp.StatusCode >= 200 && httpResp.StatusCode < 300 {
		return nil
	}

	statusCode := 0
	if httpResp != nil {
		statusCode = httpResp.StatusCode
	}

	switch {
	case statusCode == http.StatusTooManyRequests,
		statusCode == http.StatusServiceUnavailable,
		statusCode == http.StatusGatewayTimeout,
		statusCode == http.StatusBadGateway:
		return &APIError{
			Type:       APIErrorTransient,
			StatusCode: statusCode,
			Message:    fmt.Sprintf("%s: transient error (HTTP %d)", method, statusCode),
			Err:        err,
		}
	case statusCode == http.StatusConflict:
		return &APIError{
			Type:       APIErrorTransient,
			StatusCode: statusCode,
			Message:    fmt.Sprintf("%s: conflict (HTTP 409), resource being modified", method),
			Err:        err,
		}
	case statusCode == http.StatusNotFound:
		return &APIError{
			Type:       APIErrorNotFound,
			StatusCode: statusCode,
			Message:    fmt.Sprintf("%s: resource not found (HTTP 404)", method),
			Err:        err,
		}
	case statusCode == http.StatusBadRequest:
		return &APIError{
			Type:       APIErrorTerminal,
			StatusCode: statusCode,
			Message:    fmt.Sprintf("%s: bad request (HTTP 400)", method),
			Err:        err,
		}
	case statusCode >= 400:
		return &APIError{
			Type:       APIErrorTransient,
			StatusCode: statusCode,
			Message:    fmt.Sprintf("%s: server error (HTTP %d)", method, statusCode),
			Err:        err,
		}
	default:
		// No HTTP response (timeout, connection refused, etc.)
		return &APIError{
			Type:       APIErrorTransient,
			StatusCode: 0,
			Message:    fmt.Sprintf("%s: connection error", method),
			Err:        err,
		}
	}
}

// RequeueAfterForAttempt returns an exponential backoff duration for a given retry attempt.
// Caps at maxBackoff.
func RequeueAfterForAttempt(attempt int) time.Duration {
	const maxBackoff = 60 * time.Second
	backoff := time.Duration(1<<uint(attempt)) * time.Second
	if backoff > maxBackoff {
		return maxBackoff
	}
	return backoff
}
