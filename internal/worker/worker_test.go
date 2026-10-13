package worker_test

import (
	"context"
	"testing"
	"time"

	"github.com/rachmanzz/subzero/internal/worker"
	"github.com/rachmanzz/subzero/pkg/protocol"
	"github.com/rachmanzz/subzero/pkg/transport"
	_ "github.com/rachmanzz/subzero/pkg/transport/inproc"
)

func TestWorkerExecutionRoundTrip(t *testing.T) {
	tr, err := transport.Get("inproc")
	if err != nil {
		t.Fatal(err)
	}

	secretKey := []byte("worker-secret-key-32-bytes-subzero!")
	endpoint := "worker-test-pipe-1"

	l, err := tr.Listen(context.Background(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	w := worker.New(worker.Config{
		SecretKey:   secretKey,
		IdleTimeout: 10 * time.Second,
	}, func(ctx context.Context, task *protocol.TaskMessage) (*protocol.ResultMessage, error) {
		return &protocol.ResultMessage{
			TaskID:        task.TaskID,
			IsSuccess:     true,
			ProcessedRows: 1250,
		}, nil
	})
	defer w.Close()

	go func() {
		_ = w.Serve(l)
	}()

	clientConn, err := tr.Dial(context.Background(), endpoint)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer clientConn.Close()

	task := &protocol.TaskMessage{
		TaskID:       "task-uuid-999",
		TenantID:     "tenant-xyz",
		ActionTarget: "excel-parser",
		SourceURI:    "file:///tmp/data.xlsx",
	}

	taskBytes, err := protocol.EncodeTask(task, secretKey)
	if err != nil {
		t.Fatal(err)
	}

	if err := protocol.WriteFrame(clientConn, taskBytes, secretKey); err != nil {
		t.Fatalf("WriteFrame failed: %v", err)
	}

	resPayload, err := protocol.ReadFrame(clientConn, secretKey, 0)
	if err != nil {
		t.Fatalf("ReadFrame failed: %v", err)
	}

	result, err := protocol.DecodeResult(resPayload)
	if err != nil {
		t.Fatal(err)
	}

	if result.TaskID != task.TaskID || !result.IsSuccess || result.ProcessedRows != 1250 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestWorkerIdleWatchdogAutoTermination(t *testing.T) {
	tr, err := transport.Get("inproc")
	if err != nil {
		t.Fatal(err)
	}

	endpoint := "worker-watchdog-pipe"
	l, err := tr.Listen(context.Background(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	terminatedCh := make(chan struct{})

	w := worker.New(worker.Config{
		SecretKey:   []byte("secret"),
		IdleTimeout: 50 * time.Millisecond,
		OnTerminate: func() {
			close(terminatedCh)
		},
	}, nil)

	go func() {
		_ = w.Serve(l)
	}()

	select {
	case <-terminatedCh:
		// Success: watchdog fired auto-termination
	case <-time.After(500 * time.Millisecond):
		t.Fatal("worker did not auto-terminate within timeout window")
	}
}

func TestWorkerClose_WithLingeringIdleConnection(t *testing.T) {
	tr, err := transport.Get("inproc")
	if err != nil {
		t.Fatal(err)
	}

	endpoint := "worker-lingering-pipe"
	l, err := tr.Listen(context.Background(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	w := worker.New(worker.Config{
		SecretKey:   []byte("secret"),
		IdleTimeout: 10 * time.Second,
	}, nil)

	go func() {
		_ = w.Serve(l)
	}()

	// Connect a client that hangs without sending any bytes
	clientConn, err := tr.Dial(context.Background(), endpoint)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer clientConn.Close()

	// Wait briefly so the worker accepts the connection
	time.Sleep(20 * time.Millisecond)

	// Close worker: must NOT deadlock waiting for clientConn
	closeDone := make(chan error, 1)
	go func() {
		closeDone <- w.Close()
	}()

	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("w.Close returned error: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("DEADLOCK DETECTED: w.Close hung due to lingering idle connection")
	}
}
