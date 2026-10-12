package sdk_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/rachmanzz/subzero/pkg/sdk"
)

// csvUppercaseTransformer implements a real data transformer parsing CSV lines,
// converting text columns to uppercase, and counting processed records.
type csvUppercaseTransformer struct {
	processedRows int
	transformed   []byte
	hasReceived   bool
}

func (c *csvUppercaseTransformer) Receive(ctx context.Context, req *sdk.Request) error {
	if req == nil {
		return errors.New("request cannot be nil")
	}
	lines := strings.Split(strings.TrimSpace(string(req.Payload)), "\n")
	var outLines []string
	var count int
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// uppercase fields
		outLines = append(outLines, strings.ToUpper(trimmed))
		count++
	}
	c.processedRows = count
	c.transformed = []byte(strings.Join(outLines, "\n"))
	c.hasReceived = true
	return nil
}

func (c *csvUppercaseTransformer) Return(ctx context.Context) (*sdk.Response, error) {
	if !c.hasReceived {
		return nil, errors.New("cannot return response before receive is invoked")
	}
	return &sdk.Response{
		IsSuccess:     true,
		ProcessedRows: c.processedRows,
		Data:          c.transformed,
		Metadata: map[string]string{
			"encoding": "utf-8",
			"action":   "uppercase",
		},
	}, nil
}

func TestTransformerInterfaceReceiveAndReturn(t *testing.T) {
	trans := &csvUppercaseTransformer{}

	fn := sdk.FromTransformer(trans)

	csvInput := "id,name,role\n1,alice,developer\n2,bob,tester\n3,carol,architect"
	req := &sdk.Request{
		TaskID:       "task-123",
		TenantID:     "tenant-acme",
		ActionTarget: "csv-normalizer",
		Payload:      []byte(csvInput),
	}

	resp, err := fn(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !resp.IsSuccess {
		t.Fatalf("expected success, got failure: %s", resp.ErrorMessage)
	}

	// 4 lines total (header + 3 records)
	if resp.ProcessedRows != 4 {
		t.Fatalf("expected 4 processed rows, got: %d", resp.ProcessedRows)
	}

	expectedTransformed := "ID,NAME,ROLE\n1,ALICE,DEVELOPER\n2,BOB,TESTER\n3,CAROL,ARCHITECT"
	if string(resp.Data) != expectedTransformed {
		t.Fatalf("transformed output mismatch:\nwant:\n%s\ngot:\n%s", expectedTransformed, string(resp.Data))
	}

	if resp.Metadata["action"] != "uppercase" {
		t.Fatalf("expected metadata action 'uppercase', got: %v", resp.Metadata)
	}
}

func TestReadRequestFromEnv(t *testing.T) {
	os.Setenv("SUBZERO_TASK_ID", "task-env-1")
	os.Setenv("SUBZERO_TENANT_ID", "tenant-alpha")
	os.Setenv("SUBZERO_ACTION_TARGET", "target-norm")
	os.Setenv("SUBZERO_SOURCE_URI", "s3://bucket/key.csv")
	os.Setenv("SUBZERO_CFG_HEADER_ROW", "1")
	defer func() {
		os.Unsetenv("SUBZERO_TASK_ID")
		os.Unsetenv("SUBZERO_TENANT_ID")
		os.Unsetenv("SUBZERO_ACTION_TARGET")
		os.Unsetenv("SUBZERO_SOURCE_URI")
		os.Unsetenv("SUBZERO_CFG_HEADER_ROW")
	}()

	req, err := sdk.ReadRequestFromEnv()
	if err != nil {
		t.Fatalf("ReadRequestFromEnv failed: %v", err)
	}

	if req.TaskID != "task-env-1" || req.TenantID != "tenant-alpha" {
		t.Fatalf("unexpected request from env: %+v", req)
	}

	if req.CustomConfig["header_row"] != "1" {
		t.Fatalf("expected custom config header_row=1, got: %v", req.CustomConfig)
	}
}
