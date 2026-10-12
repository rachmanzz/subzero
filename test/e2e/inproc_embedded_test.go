package e2e_test

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/rachmanzz/subzero/internal/worker"
	"github.com/rachmanzz/subzero/pkg/producer"
	"github.com/rachmanzz/subzero/pkg/protocol"
	"github.com/rachmanzz/subzero/pkg/transport"
	_ "github.com/rachmanzz/subzero/pkg/transport/inproc"
)

// TestInprocEmbeddedRuntime verifies PRD Section 5.2:
// In-Memory Pipe (inproc_pipe): Zero network overhead communication based on Go pipeline (net.Pipe)
// for unit testing and embedded runtime integration.
func TestInprocEmbeddedRuntime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	inprocTr, err := transport.Get("inproc")
	if err != nil {
		t.Fatalf("transport.Get inproc failed: %v", err)
	}

	secretKey := []byte("inproc-secret-key-32b-subzero-ok!")
	supervisorEndpoint := "embedded.supervisor"
	workerEndpoint := "embedded.worker"

	// 1. Start Embedded Worker listening on inproc pipe
	workerListener, err := inprocTr.Listen(ctx, workerEndpoint)
	if err != nil {
		t.Fatalf("Worker Listen inproc failed: %v", err)
	}
	defer workerListener.Close()

	w := worker.New(worker.Config{
		SecretKey:   secretKey,
		IdleTimeout: 1 * time.Minute,
	}, func(ctx context.Context, task *protocol.TaskMessage) (*protocol.ResultMessage, error) {
		lines := strings.Split(strings.TrimSpace(task.CustomConfig["raw_lines"]), "\n")
		rowCount := 0
		if len(lines) > 1 {
			rowCount = len(lines) - 1 // subtract header
		}
		return &protocol.ResultMessage{
			TaskID:        task.TaskID,
			IsSuccess:     true,
			ProcessedRows: int64(rowCount),
		}, nil
	})
	defer w.Close()

	go func() {
		_ = w.Serve(workerListener)
	}()

	// 2. Start Embedded Supervisor Daemon listening on inproc pipe
	supervisorListener, err := inprocTr.Listen(ctx, supervisorEndpoint)
	if err != nil {
		t.Fatalf("Supervisor Listen inproc failed: %v", err)
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

				// Relay directly to embedded worker over inproc
				wConn, err := inprocTr.Dial(ctx, workerEndpoint)
				if err != nil {
					errRes := &protocol.ResultMessage{
						IsSuccess:    false,
						ErrorMessage: err.Error(),
					}
					data, _ := protocol.EncodeResult(errRes)
					_ = protocol.WriteFrame(c, data, secretKey)
					return
				}
				defer wConn.Close()

				if err := protocol.WriteFrame(wConn, rawPayload, secretKey); err != nil {
					return
				}

				resBytes, err := protocol.ReadFrame(wConn, secretKey, 0)
				if err != nil {
					return
				}

				_ = protocol.WriteFrame(c, resBytes, secretKey)
			}(conn)
		}
	}()

	// 3. Connect Producer Client via inproc scheme
	client, err := producer.New(producer.Config{
		Scheme:    "inproc",
		Endpoint:  supervisorEndpoint,
		SecretKey: secretKey,
		Timeout:   5 * time.Second,
	})
	if err != nil {
		t.Fatalf("producer.New inproc failed: %v", err)
	}

	task := &protocol.TaskMessage{
		TaskID:       "task-inproc-001",
		TenantID:     "tenant-inproc",
		ActionTarget: "embedded-transformer",
		SourceURI:    "inproc://data-stream",
		CustomConfig: map[string]string{
			"raw_lines": "header1,header2\nrow1_col1,row1_col2\nrow2_col1,row2_col2\nrow3_col1,row3_col2\n",
		},
	}

	start := time.Now()
	res, err := client.SubmitTask(ctx, task)
	if err != nil {
		t.Fatalf("SubmitTask inproc failed: %v", err)
	}
	latency := time.Since(start)

	t.Logf("[Embedded Inproc Runtime PRD 5.2] Task processed in %v (ProcessedRows: %d)", latency, res.ProcessedRows)

	if !res.IsSuccess || res.ProcessedRows != 3 {
		t.Errorf("unexpected result: %+v, expected ProcessedRows: 3", res)
	}
}
