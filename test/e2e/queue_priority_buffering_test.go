package e2e_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rachmanzz/fiber-extras/v3/queue"
	"github.com/rachmanzz/subzero/internal/supervisor"
	"github.com/rachmanzz/subzero/pkg/producer"
	"github.com/rachmanzz/subzero/pkg/protocol"
)

// TestQueueTrafficSpikeBuffering verifies PRD Section 4 & Section 5.2:
// Gateway/Producer buffers a surge of tasks in the persistent queue adapter (traffic spike buffering)
// and the Supervisor Dispatcher drains the buffer reliably without task loss.
func TestQueueTrafficSpikeBuffering(t *testing.T) {
	memDriver := queue.NewMemoryDriver()

	prodClient, err := producer.New(producer.Config{
		QueueDriver: memDriver,
	})
	if err != nil {
		t.Fatalf("producer.New failed: %v", err)
	}

	topic := "subzero.spike.transforms"
	totalTasks := 40
	var processedCount int32
	var wg sync.WaitGroup
	wg.Add(totalTasks)

	// 1. Simulate Traffic Spike: Enqueue 40 tasks BEFORE starting supervisor dispatcher
	t.Logf("[Traffic Spike] Enqueueing %d tasks into queue buffer before supervisor starts...", totalTasks)
	ctx := context.Background()

	for i := 1; i <= totalTasks; i++ {
		task := &protocol.TaskMessage{
			TaskID:       fmt.Sprintf("task-spike-%03d", i),
			TenantID:     fmt.Sprintf("tenant-%d", i%5),
			ActionTarget: "document-normalizer",
			SourceURI:    fmt.Sprintf("s3://vault/doc-%03d.csv", i),
		}

		if err := prodClient.EnqueueTask(ctx, topic, task); err != nil {
			t.Fatalf("failed to buffer task %d: %v", i, err)
		}
	}

	// 2. Start Supervisor Dispatcher with Queue Consumer
	dispatcher := supervisor.NewDispatcher(supervisor.DispatcherConfig{
		Concurrency: 8,
		QueueSize:   128,
		SubLimit:    2,
		QueueDriver: memDriver,
	})

	dispatcher.RegisterQueueHandler(topic, func(ctx context.Context, task *protocol.TaskMessage, pool supervisor.SubWorkerPool) error {
		defer wg.Done()
		atomic.AddInt32(&processedCount, 1)
		return nil
	})

	t.Log("[Supervisor] Starting Dispatcher to drain traffic spike buffer...")
	dispatcher.Start()
	defer dispatcher.Stop()

	// 3. Await all buffered tasks to be processed
	doneCh := make(chan struct{})
	go func() {
		wg.Wait()
		close(doneCh)
	}()

	select {
	case <-doneCh:
		t.Logf("[Success] All %d tasks successfully drained from spike buffer", totalTasks)
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for tasks to drain. Processed: %d/%d", atomic.LoadInt32(&processedCount), totalTasks)
	}

	if atomic.LoadInt32(&processedCount) != int32(totalTasks) {
		t.Fatalf("expected %d processed tasks, got %d", totalTasks, atomic.LoadInt32(&processedCount))
	}
}

// TestQueueTenantPriorityOrdering verifies PRD Section 5.2:
// Multi-tenant priority ordering: High-priority tasks (VIP tenants) are processed ahead of Low-priority tasks.
func TestQueueTenantPriorityOrdering(t *testing.T) {
	memDriver := queue.NewMemoryDriver()

	prodClient, err := producer.New(producer.Config{
		QueueDriver: memDriver,
	})
	if err != nil {
		t.Fatalf("producer.New failed: %v", err)
	}

	topic := "subzero.priority.transforms"

	// 1. Enqueue tasks in REVERSE priority order while queue is unconsumed
	// Order enqueued: Low -> Default -> High
	lowTask := &protocol.TaskMessage{
		TaskID:       "task-tier3-low",
		TenantID:     "tenant-free",
		ActionTarget: "bulk-parser",
	}

	defaultTask := &protocol.TaskMessage{
		TaskID:       "task-tier2-default",
		TenantID:     "tenant-standard",
		ActionTarget: "bulk-parser",
	}

	highTask := &protocol.TaskMessage{
		TaskID:       "task-tier1-high",
		TenantID:     "tenant-vip",
		ActionTarget: "bulk-parser",
	}

	ctx := context.Background()
	// Enqueue Low first
	if err := prodClient.EnqueueTask(ctx, topic, lowTask, queue.WithPriority(queue.PriorityLow)); err != nil {
		t.Fatal(err)
	}
	// Enqueue Default second
	if err := prodClient.EnqueueTask(ctx, topic, defaultTask, queue.WithPriority(queue.PriorityDefault)); err != nil {
		t.Fatal(err)
	}
	// Enqueue High last
	if err := prodClient.EnqueueTask(ctx, topic, highTask, queue.WithPriority(queue.PriorityHigh)); err != nil {
		t.Fatal(err)
	}

	// 2. Consume sequentially (Concurrency: 1) to inspect execution order
	var executionOrder []string
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(3)

	dispatcher := supervisor.NewDispatcher(supervisor.DispatcherConfig{
		Concurrency: 1, // Single worker to strictly evaluate priority dequeue order
		QueueSize:   16,
		QueueDriver: memDriver,
	})

	dispatcher.RegisterQueueHandler(topic, func(ctx context.Context, task *protocol.TaskMessage, pool supervisor.SubWorkerPool) error {
		mu.Lock()
		executionOrder = append(executionOrder, task.TaskID)
		mu.Unlock()
		wg.Done()
		return nil
	})

	dispatcher.Start()
	defer dispatcher.Stop()

	doneCh := make(chan struct{})
	go func() {
		wg.Wait()
		close(doneCh)
	}()

	select {
	case <-doneCh:
	case <-time.After(4 * time.Second):
		t.Fatalf("timed out waiting for priority tasks to be processed")
	}

	mu.Lock()
	defer mu.Unlock()

	t.Logf("[Priority Dequeue Order] %v", executionOrder)
	if len(executionOrder) != 3 {
		t.Fatalf("expected 3 tasks executed, got %d", len(executionOrder))
	}

	// High-priority task must execute first despite being enqueued last
	if executionOrder[0] != "task-tier1-high" {
		t.Errorf("expected high-priority task first, but got: %s", executionOrder[0])
	}
	// Low-priority task must execute last despite being enqueued first
	if executionOrder[2] != "task-tier3-low" {
		t.Errorf("expected low-priority task last, but got: %s", executionOrder[2])
	}
}

// TestQueueGuaranteedDeliveryRetry verifies PRD Section 5.2:
// Guaranteed task delivery with exponential backoff retries when transient errors occur.
func TestQueueGuaranteedDeliveryRetry(t *testing.T) {
	memDriver := queue.NewMemoryDriver()

	prodClient, err := producer.New(producer.Config{
		QueueDriver: memDriver,
	})
	if err != nil {
		t.Fatalf("producer.New failed: %v", err)
	}

	topic := "subzero.retry.transforms"
	var attempts int32
	var successDone sync.WaitGroup
	successDone.Add(1)

	dispatcher := supervisor.NewDispatcher(supervisor.DispatcherConfig{
		Concurrency: 2,
		QueueSize:   16,
		QueueDriver: memDriver,
	})

	// Handler fails on attempt 1, succeeds on attempt 2
	dispatcher.RegisterQueueHandler(topic, func(ctx context.Context, task *protocol.TaskMessage, pool supervisor.SubWorkerPool) error {
		att := atomic.AddInt32(&attempts, 1)
		if att == 1 {
			return errors.New("transient network hiccup connecting to storage")
		}
		successDone.Done()
		return nil
	})

	dispatcher.Start()
	defer dispatcher.Stop()

	task := &protocol.TaskMessage{
		TaskID:       "task-retry-01",
		TenantID:     "tenant-resilient",
		ActionTarget: "reliable-transformer",
	}

	// Enqueue with MaxAttempts: 3
	if err := prodClient.EnqueueTask(context.Background(), topic, task, queue.WithMaxAttempts(3)); err != nil {
		t.Fatalf("EnqueueTask failed: %v", err)
	}

	doneCh := make(chan struct{})
	go func() {
		successDone.Wait()
		close(doneCh)
	}()

	select {
	case <-doneCh:
		t.Logf("[Success] Task succeeded after %d attempts (Guaranteed delivery verified)", atomic.LoadInt32(&attempts))
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for task retry. Attempts made: %d", atomic.LoadInt32(&attempts))
	}

	if atomic.LoadInt32(&attempts) != 2 {
		t.Errorf("expected task to succeed on attempt 2, got %d attempts", atomic.LoadInt32(&attempts))
	}
}
