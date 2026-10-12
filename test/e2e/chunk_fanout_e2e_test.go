package e2e_test

import (
	"context"
	"net"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rachmanzz/subzero/internal/supervisor"
	"github.com/rachmanzz/subzero/internal/worker"
	"github.com/rachmanzz/subzero/pkg/producer"
	"github.com/rachmanzz/subzero/pkg/protocol"
	"github.com/rachmanzz/subzero/pkg/transport"
	_ "github.com/rachmanzz/subzero/pkg/transport/ipc"
)

// TestChunkFanOutParallelE2E verifies PRD Section 5.1 & Section 5.4:
// Parallelization & Chunking: Massive multi-sheet and row-range documents are split
// into parallel sub-tasks dispatched concurrently across a SubWorkerPool with fan-in aggregation.
func TestChunkFanOutParallelE2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	tmpDir := t.TempDir()
	secretKey := []byte("chunk-fanout-secret-32b-subzero!")
	supervisorSock := filepath.Join(tmpDir, "supervisor-chunk.sock")
	workerSock := filepath.Join(tmpDir, "worker-chunk.sock")

	ipcTr, err := transport.Get("unix")
	if err != nil {
		t.Fatalf("transport.Get unix failed: %v", err)
	}

	// 1. Worker Handler simulates parsing individual sheets / chunks
	var subTasksExecuted int32
	var maxObservedSubWorkers int32
	var activeSubWorkers int32
	var mu sync.Mutex

	workerListener, err := ipcTr.Listen(ctx, workerSock)
	if err != nil {
		t.Fatalf("Worker Listen failed: %v", err)
	}
	defer workerListener.Close()

	w := worker.New(worker.Config{
		SecretKey:   secretKey,
		IdleTimeout: 1 * time.Minute,
	}, func(ctx context.Context, task *protocol.TaskMessage) (*protocol.ResultMessage, error) {
		curr := atomic.AddInt32(&activeSubWorkers, 1)
		mu.Lock()
		if curr > maxObservedSubWorkers {
			maxObservedSubWorkers = curr
		}
		mu.Unlock()

		// Small delay to verify concurrent parallelism
		time.Sleep(10 * time.Millisecond)

		atomic.AddInt32(&activeSubWorkers, -1)
		atomic.AddInt32(&subTasksExecuted, 1)

		rows := int64(100)
		if rStr, ok := task.CustomConfig["chunk_rows"]; ok {
			if rStr == "250" {
				rows = 250
			}
		}

		return &protocol.ResultMessage{
			TaskID:        task.TaskID,
			IsSuccess:     true,
			ProcessedRows: rows,
		}, nil
	})
	defer w.Close()

	go func() {
		_ = w.Serve(workerListener)
	}()

	// 2. Supervisor with Dispatcher (concurrency: 8, sub-worker limit: 8)
	dispatcher := supervisor.NewDispatcher(supervisor.DispatcherConfig{
		Concurrency: 8,
		QueueSize:   64,
		SubLimit:    8,
	})
	dispatcher.Start()
	defer dispatcher.Stop()

	// 3. Supervisor Listener with Chunk Fan-Out Plan execution
	supervisorListener, err := ipcTr.Listen(ctx, supervisorSock)
	if err != nil {
		t.Fatalf("Supervisor Listen failed: %v", err)
	}
	defer supervisorListener.Close()

	go func() {
		for {
			conn, err := supervisorListener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				rawPayload, err := protocol.ReadFrame(c, secretKey, 0)
				if err != nil {
					return
				}

				taskMsg, err := protocol.DecodeTask(rawPayload)
				if err != nil {
					return
				}

				// Check chunk fan-out
				plan, isChunked := supervisor.BuildChunkFanOutPlan(taskMsg)
				if isChunked {
					runner := supervisor.TaskRunnerFunc(func(subCtx context.Context, subTask *protocol.TaskMessage) (*protocol.ResultMessage, error) {
						wConn, err := ipcTr.Dial(subCtx, workerSock)
						if err != nil {
							return nil, err
						}
						defer wConn.Close()

						subBytes, err := protocol.EncodeTask(subTask, secretKey)
						if err != nil {
							return nil, err
						}
						if err := protocol.WriteFrame(wConn, subBytes, secretKey); err != nil {
							return nil, err
						}
						resBytes, err := protocol.ReadFrame(wConn, secretKey, 0)
						if err != nil {
							return nil, err
						}
						return protocol.DecodeResult(resBytes)
					})

					chunkedTask := supervisor.NewChunkedTask(plan, runner)
					_ = dispatcher.ExecuteSync(ctx, chunkedTask)
					resBytes, _ := protocol.EncodeResult(chunkedTask.Result())
					_ = protocol.WriteFrame(c, resBytes, secretKey)
					return
				}

				// Direct single task relay
				wConn, err := ipcTr.Dial(ctx, workerSock)
				if err != nil {
					return
				}
				defer wConn.Close()
				_ = protocol.WriteFrame(wConn, rawPayload, secretKey)
				resBytes, _ := protocol.ReadFrame(wConn, secretKey, 0)
				_ = protocol.WriteFrame(c, resBytes, secretKey)
			}(conn)
		}
	}()

	// 4. Producer Client
	client, err := producer.New(producer.Config{
		Scheme:    "unix",
		Endpoint:  supervisorSock,
		SecretKey: secretKey,
		Timeout:   10 * time.Second,
	})
	if err != nil {
		t.Fatalf("producer.New failed: %v", err)
	}

	// -------------------------------------------------------------
	// SCENARIO 1: Multi-Sheet Fan-Out (Q1, Q2, Q3, Q4)
	// -------------------------------------------------------------
	multiSheetTask := &protocol.TaskMessage{
		TaskID:       "task-multi-sheet-001",
		TenantID:     "tenant-enterprise",
		ActionTarget: "excel-parser",
		SourceURI:    "s3://storage/annual_report_2026.xlsx",
		CustomConfig: map[string]string{
			"sheets": "Q1,Q2,Q3,Q4",
		},
	}

	t.Log("[Scenario 1] Dispatching multi-sheet document (Q1, Q2, Q3, Q4)...")
	start := time.Now()
	resSheet, err := client.SubmitTask(ctx, multiSheetTask)
	if err != nil {
		t.Fatalf("SubmitTask multi-sheet failed: %v", err)
	}
	elapsed := time.Since(start)

	if !resSheet.IsSuccess {
		t.Fatalf("expected success, got error: %s", resSheet.ErrorMessage)
	}
	// 4 sheets * 100 rows = 400 rows
	if resSheet.ProcessedRows != 400 {
		t.Fatalf("expected 400 aggregated rows, got %d", resSheet.ProcessedRows)
	}
	t.Logf("[Scenario 1 SUCCESS] Multi-sheet fan-out processed in %v (Aggregated rows: %d)", elapsed, resSheet.ProcessedRows)

	// -------------------------------------------------------------
	// SCENARIO 2: Row-Range Fan-Out (1000 total rows / 250 chunk rows = 4 chunks)
	// -------------------------------------------------------------
	rowRangeTask := &protocol.TaskMessage{
		TaskID:       "task-row-range-002",
		TenantID:     "tenant-fintech",
		ActionTarget: "massive-row-parser",
		SourceURI:    "s3://storage/ledger_2026.csv",
		CustomConfig: map[string]string{
			"chunk_rows": "250",
			"total_rows": "1000",
		},
	}

	t.Log("[Scenario 2] Dispatching row-range document (total: 1000, chunk: 250)...")
	start2 := time.Now()
	resRange, err := client.SubmitTask(ctx, rowRangeTask)
	if err != nil {
		t.Fatalf("SubmitTask row-range failed: %v", err)
	}
	elapsed2 := time.Since(start2)

	if !resRange.IsSuccess {
		t.Fatalf("expected success, got error: %s", resRange.ErrorMessage)
	}
	// 4 chunks * 250 rows = 1000 rows
	if resRange.ProcessedRows != 1000 {
		t.Fatalf("expected 1000 aggregated rows, got %d", resRange.ProcessedRows)
	}
	t.Logf("[Scenario 2 SUCCESS] Row-range fan-out processed in %v (Aggregated rows: %d)", elapsed2, resRange.ProcessedRows)

	// Verify parallel concurrency was achieved
	mu.Lock()
	maxWorkers := maxObservedSubWorkers
	mu.Unlock()
	t.Logf("[Concurrency Check] Max parallel sub-workers active simultaneously: %d", maxWorkers)
	if maxWorkers < 2 {
		t.Errorf("expected parallel execution (>= 2 concurrent sub-workers), got %d", maxWorkers)
	}
}
