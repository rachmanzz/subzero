package supervisor_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rachmanzz/subzero/internal/supervisor"
	"github.com/rachmanzz/subzero/pkg/protocol"
)

func TestChunkFanOut_MultiSheet(t *testing.T) {
	task := &protocol.TaskMessage{
		TaskID:       "task-spreadsheet-1",
		TenantID:     "tenant-acme",
		ActionTarget: "excel-cleaner",
		SourceURI:    "s3://bucket/financials.xlsx",
		CustomConfig: map[string]string{
			"sheets": "Q1,Q2,Q3,Q4",
		},
	}

	plan, isChunked := supervisor.BuildChunkFanOutPlan(task)
	if !isChunked {
		t.Fatal("expected task to be chunked into multiple sheets")
	}
	if len(plan.Chunks) != 4 {
		t.Fatalf("expected 4 chunks, got %d", len(plan.Chunks))
	}

	var executedChunks int64
	mockRunner := supervisor.TaskRunnerFunc(func(ctx context.Context, subTask *protocol.TaskMessage) (*protocol.ResultMessage, error) {
		atomic.AddInt64(&executedChunks, 1)
		sheet := subTask.CustomConfig["sheet"]
		if sheet == "" {
			return nil, fmt.Errorf("sheet not specified in subTask")
		}
		return &protocol.ResultMessage{
			TaskID:        subTask.TaskID,
			IsSuccess:     true,
			ProcessedRows: 250,
		}, nil
	})

	chunkedTask := supervisor.NewChunkedTask(plan, mockRunner)

	d := supervisor.NewDispatcher(supervisor.DispatcherConfig{
		Concurrency: 4,
		SubLimit:    4,
	})
	defer d.Stop()

	err := d.ExecuteSync(context.Background(), chunkedTask)
	if err != nil {
		t.Fatalf("ExecuteSync failed: %v", err)
	}

	res := chunkedTask.Result()
	if !res.IsSuccess {
		t.Fatalf("expected success, got error: %s", res.ErrorMessage)
	}
	if res.ProcessedRows != 1000 {
		t.Fatalf("expected 1000 total rows aggregated across 4 sheets, got %d", res.ProcessedRows)
	}
	if atomic.LoadInt64(&executedChunks) != 4 {
		t.Fatalf("expected 4 sub-worker invocations, got %d", atomic.LoadInt64(&executedChunks))
	}
}

func TestChunkFanOut_RowRanges(t *testing.T) {
	task := &protocol.TaskMessage{
		TaskID:       "task-csv-massive",
		TenantID:     "tenant-acme",
		ActionTarget: "csv-normalizer",
		SourceURI:    "s3://bucket/bigdata.csv",
		CustomConfig: map[string]string{
			"chunk_rows": "2000",
			"total_rows": "5500",
		},
	}

	plan, isChunked := supervisor.BuildChunkFanOutPlan(task)
	if !isChunked {
		t.Fatal("expected task to be chunked into row ranges")
	}
	// 5500 rows / 2000 chunk_rows = 3 chunks (1..2000, 2001..4000, 4001..5500)
	if len(plan.Chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d", len(plan.Chunks))
	}

	if plan.Chunks[0].RowStart != 1 || plan.Chunks[0].RowCount != 2000 {
		t.Fatalf("unexpected chunk 0: %+v", plan.Chunks[0])
	}
	if plan.Chunks[1].RowStart != 2001 || plan.Chunks[1].RowCount != 2000 {
		t.Fatalf("unexpected chunk 1: %+v", plan.Chunks[1])
	}
	if plan.Chunks[2].RowStart != 4001 || plan.Chunks[2].RowCount != 1500 {
		t.Fatalf("unexpected chunk 2: %+v", plan.Chunks[2])
	}

	mockRunner := supervisor.TaskRunnerFunc(func(ctx context.Context, subTask *protocol.TaskMessage) (*protocol.ResultMessage, error) {
		// Mock processing
		return &protocol.ResultMessage{
			TaskID:        subTask.TaskID,
			IsSuccess:     true,
			ProcessedRows: 1500,
		}, nil
	})

	chunkedTask := supervisor.NewChunkedTask(plan, mockRunner)

	d := supervisor.NewDispatcher(supervisor.DispatcherConfig{
		Concurrency: 4,
		SubLimit:    4,
	})
	defer d.Stop()

	err := d.ExecuteSync(context.Background(), chunkedTask)
	if err != nil {
		t.Fatalf("ExecuteSync failed: %v", err)
	}

	res := chunkedTask.Result()
	if !res.IsSuccess {
		t.Fatalf("expected success, got error: %s", res.ErrorMessage)
	}
	if res.ProcessedRows != 4500 {
		t.Fatalf("expected 4500 total rows aggregated, got %d", res.ProcessedRows)
	}
}

func TestChunkFanOut_SingleFailurePropagates(t *testing.T) {
	task := &protocol.TaskMessage{
		TaskID:       "task-failing",
		TenantID:     "tenant-acme",
		CustomConfig: map[string]string{
			"sheets": "A,B,C",
		},
	}

	plan, _ := supervisor.BuildChunkFanOutPlan(task)
	mockRunner := supervisor.TaskRunnerFunc(func(ctx context.Context, subTask *protocol.TaskMessage) (*protocol.ResultMessage, error) {
		sheet := subTask.CustomConfig["sheet"]
		if sheet == "B" {
			return &protocol.ResultMessage{
				IsSuccess:    false,
				ErrorMessage: "corrupted formula in sheet B",
			}, nil
		}
		time.Sleep(10 * time.Millisecond)
		return &protocol.ResultMessage{IsSuccess: true, ProcessedRows: 100}, nil
	})

	chunkedTask := supervisor.NewChunkedTask(plan, mockRunner)
	d := supervisor.NewDispatcher(supervisor.DispatcherConfig{Concurrency: 4, SubLimit: 4})
	defer d.Stop()

	err := d.ExecuteSync(context.Background(), chunkedTask)
	if err == nil {
		t.Fatal("expected failure to propagate, got nil")
	}

	res := chunkedTask.Result()
	if res.IsSuccess {
		t.Fatal("expected ResultMessage.IsSuccess to be false")
	}
}
