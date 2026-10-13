// SPDX-License-Identifier: AGPL-3.0-only

package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// Request represents the incoming task metadata and raw data payload received from Subzero.
type Request struct {
	TaskID       string            `json:"task_id"`
	TenantID     string            `json:"tenant_id"`
	ActionTarget string            `json:"action_target"`
	SourceURI    string            `json:"source_uri"`
	CustomConfig map[string]string `json:"custom_config,omitempty"`
	Payload      []byte            `json:"payload,omitempty"`
}

// Response represents the normalized data, row metrics, and execution status returned to Subzero.
type Response struct {
	IsSuccess     bool              `json:"is_success"`
	ProcessedRows int               `json:"processed_rows"`
	Data          []byte            `json:"data,omitempty"`
	OutputURI     string            `json:"output_uri,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	ErrorMessage  string            `json:"error_message,omitempty"`
}

// Transformer defines the two-stage interface for receiving data and returning transformed results.
type Transformer interface {
	// Receive ingests and validates the incoming task request and raw payload from Subzero.
	Receive(ctx context.Context, req *Request) error

	// Return produces and outputs the normalized data and row metrics back to Subzero.
	Return(ctx context.Context) (*Response, error)
}

// TransformFunc is a functional handler that receives a Request and returns a Response.
type TransformFunc func(ctx context.Context, req *Request) (*Response, error)

// TransformerFuncAdapter adapts a single TransformFunc into the Transformer interface.
type TransformerFuncAdapter struct {
	fn     TransformFunc
	req    *Request
	result *Response
}

// NewTransformer adapts a TransformFunc into a Transformer.
func NewTransformer(fn TransformFunc) Transformer {
	return &TransformerFuncAdapter{fn: fn}
}

func (a *TransformerFuncAdapter) Receive(ctx context.Context, req *Request) error {
	a.req = req
	return nil
}

func (a *TransformerFuncAdapter) Return(ctx context.Context) (*Response, error) {
	return a.fn(ctx, a.req)
}

// FromTransformer converts a Transformer into a TransformFunc.
func FromTransformer(t Transformer) TransformFunc {
	return func(ctx context.Context, req *Request) (*Response, error) {
		if err := t.Receive(ctx, req); err != nil {
			return nil, fmt.Errorf("transformer receive failed: %w", err)
		}
		return t.Return(ctx)
	}
}

// ReadRequestFromEnv reads standard Subzero environment variables and standard input into a Request.
func ReadRequestFromEnv() (*Request, error) {
	req := &Request{
		TaskID:       os.Getenv("SUBZERO_TASK_ID"),
		TenantID:     os.Getenv("SUBZERO_TENANT_ID"),
		ActionTarget: os.Getenv("SUBZERO_ACTION_TARGET"),
		SourceURI:    os.Getenv("SUBZERO_SOURCE_URI"),
		CustomConfig: make(map[string]string),
	}

	for _, env := range os.Environ() {
		if strings.HasPrefix(env, "SUBZERO_CFG_") {
			parts := strings.SplitN(env, "=", 2)
			if len(parts) == 2 {
				key := strings.ToLower(strings.TrimPrefix(parts[0], "SUBZERO_CFG_"))
				req.CustomConfig[key] = parts[1]
			}
		}
	}

	// Read input from Stdin if available
	stdinBytes, err := io.ReadAll(os.Stdin)
	if err == nil && len(stdinBytes) > 0 {
		// Check if Stdin is a full JSON Request wrapper
		var wrapped Request
		if bytes.HasPrefix(bytes.TrimSpace(stdinBytes), []byte("{")) && json.Unmarshal(stdinBytes, &wrapped) == nil && (wrapped.TaskID != "" || len(wrapped.Payload) > 0) {
			if wrapped.TaskID != "" {
				req.TaskID = wrapped.TaskID
			}
			if wrapped.TenantID != "" {
				req.TenantID = wrapped.TenantID
			}
			if wrapped.ActionTarget != "" {
				req.ActionTarget = wrapped.ActionTarget
			}
			if wrapped.SourceURI != "" {
				req.SourceURI = wrapped.SourceURI
			}
			if len(wrapped.CustomConfig) > 0 {
				for k, v := range wrapped.CustomConfig {
					req.CustomConfig[k] = v
				}
			}
			req.Payload = wrapped.Payload
		} else {
			req.Payload = stdinBytes
		}
	}

	return req, nil
}

// Run executes a Transformer interface, automatically managing I/O exchange with Subzero.
func Run(t Transformer) {
	RunFunc(FromTransformer(t))
}

// Handle is an alias for RunFunc, providing an idiomatic functional entrypoint.
func Handle(fn TransformFunc) {
	RunFunc(fn)
}

// RunFunc executes a TransformFunc, reading the request and writing the structured response to Stdout.
func RunFunc(fn TransformFunc) {
	ctx := context.Background()
	req, err := ReadRequestFromEnv()
	if err != nil {
		emitError(fmt.Sprintf("sdk: read request failed: %v", err))
		os.Exit(1)
	}

	resp, err := fn(ctx, req)
	if err != nil {
		emitError(err.Error())
		os.Exit(1)
	}

	if resp == nil {
		resp = &Response{IsSuccess: true}
	}

	// Output structured JSON response to Stdout
	enc := json.NewEncoder(os.Stdout)
	if err := enc.Encode(resp); err != nil {
		emitError(fmt.Sprintf("sdk: encode response failed: %v", err))
		os.Exit(1)
	}
}

func emitError(msg string) {
	fmt.Fprintf(os.Stderr, "Error: %s\n", msg)
	_ = json.NewEncoder(os.Stdout).Encode(&Response{
		IsSuccess:    false,
		ErrorMessage: msg,
	})
}
