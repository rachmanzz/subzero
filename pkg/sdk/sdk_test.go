package sdk_test

import (
	"context"
	"os"
	"testing"

	"github.com/rachmanzz/subzero/pkg/sdk"
)

type mockTransformer struct {
	receivedReq *sdk.Request
	outputData  []byte
	rowsCount   int
}

func (m *mockTransformer) Receive(ctx context.Context, req *sdk.Request) error {
	m.receivedReq = req
	return nil
}

func (m *mockTransformer) Return(ctx context.Context) (*sdk.Response, error) {
	return &sdk.Response{
		IsSuccess:     true,
		ProcessedRows: m.rowsCount,
		Data:          m.outputData,
	}, nil
}

func TestTransformerInterfaceReceiveAndReturn(t *testing.T) {
	trans := &mockTransformer{
		outputData: []byte("canonical-output-data"),
		rowsCount:  42,
	}

	fn := sdk.FromTransformer(trans)

	req := &sdk.Request{
		TaskID:       "task-123",
		TenantID:     "tenant-acme",
		ActionTarget: "parser-x",
		Payload:      []byte("raw-input-data"),
	}

	resp, err := fn(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if trans.receivedReq == nil || trans.receivedReq.TaskID != "task-123" {
		t.Fatalf("expected request to be received, got: %+v", trans.receivedReq)
	}

	if !resp.IsSuccess || resp.ProcessedRows != 42 || string(resp.Data) != "canonical-output-data" {
		t.Fatalf("unexpected response returned: %+v", resp)
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
