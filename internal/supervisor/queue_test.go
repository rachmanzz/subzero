package supervisor_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rachmanzz/gosuite/v3/queue"
	"github.com/rachmanzz/subzero/internal/supervisor"
	"github.com/rachmanzz/subzero/pkg/producer"
	"github.com/rachmanzz/subzero/pkg/protocol"
)

func TestDispatcherQueueConsumerEndToEnd(t *testing.T) {
	memDriver := queue.NewMemoryDriver()

	d := supervisor.NewDispatcher(supervisor.DispatcherConfig{
		Concurrency: 4,
		QueueSize:   16,
		SubLimit:    4,
		QueueDriver: memDriver,
	})

	var processedCount int64
	var subTaskCount int64
	var wg sync.WaitGroup
	wg.Add(2)

	topic := "subzero.data.transforms"

	d.RegisterQueueHandler(topic, func(ctx context.Context, task *protocol.TaskMessage, pool supervisor.SubWorkerPool) error {
		defer wg.Done()
		atomic.AddInt64(&processedCount, 1)

		// Verify sub-worker fan-out inside the queue handler
		for i := 0; i < 3; i++ {
			pool.Go(func(ctx context.Context) error {
				atomic.AddInt64(&subTaskCount, 1)
				return nil
			})
		}

		if err := pool.Wait(); err != nil {
			return err
		}

		return nil
	})

	d.Start()
	defer d.Stop()

	// Use producer client to publish tasks into the queue
	prodClient, err := producer.New(producer.Config{
		QueueDriver: memDriver,
	})
	if err != nil {
		t.Fatalf("producer.New failed: %v", err)
	}

	task1 := &protocol.TaskMessage{
		TaskID:       "task-q-1",
		TenantID:     "tenant-alpha",
		ActionTarget: "wasm-transform-bank",
		SourceURI:    "s3://storage/file1.xlsx",
	}

	task2 := &protocol.TaskMessage{
		TaskID:       "task-q-2",
		TenantID:     "tenant-beta",
		ActionTarget: "wasm-transform-fin",
		SourceURI:    "s3://storage/file2.csv",
	}

	if err := prodClient.EnqueueTask(context.Background(), topic, task1, queue.WithPriority(queue.PriorityHigh)); err != nil {
		t.Fatalf("EnqueueTask 1 failed: %v", err)
	}

	if err := prodClient.EnqueueTask(context.Background(), topic, task2, queue.WithPriority(queue.PriorityDefault)); err != nil {
		t.Fatalf("EnqueueTask 2 failed: %v", err)
	}

	doneCh := make(chan struct{})
	go func() {
		wg.Wait()
		close(doneCh)
	}()

	select {
	case <-doneCh:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for queue tasks to be processed. Processed: %d", atomic.LoadInt64(&processedCount))
	}

	if atomic.LoadInt64(&processedCount) != 2 {
		t.Fatalf("expected 2 tasks processed, got %d", atomic.LoadInt64(&processedCount))
	}

	// 2 tasks * 3 sub-workers = 6 sub-tasks
	if atomic.LoadInt64(&subTaskCount) != 6 {
		t.Fatalf("expected 6 sub-tasks completed, got %d", atomic.LoadInt64(&subTaskCount))
	}
}

func TestDispatcherQueueInvalidPayload(t *testing.T) {
	memDriver := queue.NewMemoryDriver()

	d := supervisor.NewDispatcher(supervisor.DispatcherConfig{
		Concurrency: 2,
		QueueSize:   8,
		QueueDriver: memDriver,
	})

	var errorReported bool
	topic := "subzero.invalid.topic"

	d.RegisterQueueHandler(topic, func(ctx context.Context, task *protocol.TaskMessage, pool supervisor.SubWorkerPool) error {
		t.Errorf("handler should not have been called for invalid payload")
		return nil
	})

	d.Start()
	defer d.Stop()

	// Enqueue malformed JSON
	badMsg := queue.NewMessage(topic, []byte("{invalid-json-payload"))
	if err := memDriver.Enqueue(context.Background(), badMsg); err != nil {
		t.Fatalf("Enqueue badMsg failed: %v", err)
	}

	// Wait briefly to let consumer process
	time.Sleep(100 * time.Millisecond)

	_ = errorReported
}
