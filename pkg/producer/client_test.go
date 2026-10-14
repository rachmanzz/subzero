package producer_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/rachmanzz/gosuite/v3/queue"
	"github.com/rachmanzz/subzero/internal/storage/local"
	"github.com/rachmanzz/subzero/internal/worker"
	"github.com/rachmanzz/subzero/pkg/producer"
	"github.com/rachmanzz/subzero/pkg/protocol"
	"github.com/rachmanzz/subzero/pkg/transport"
	_ "github.com/rachmanzz/subzero/pkg/transport/inproc"
)

func TestProducerClientE2E(t *testing.T) {
	tr, err := transport.Get("inproc")
	if err != nil {
		t.Fatal(err)
	}

	endpoint := "producer-test-endpoint"
	secretKey := []byte("secret-key-32-bytes-subzero-prod!")

	l, err := tr.Listen(context.Background(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	// Spin up worker
	w := worker.New(worker.Config{
		SecretKey:   secretKey,
		IdleTimeout: 5 * time.Second,
	}, func(ctx context.Context, task *protocol.TaskMessage) (*protocol.ResultMessage, error) {
		return &protocol.ResultMessage{
			TaskID:        task.TaskID,
			IsSuccess:     true,
			ProcessedRows: 5000,
		}, nil
	})
	defer w.Close()

	go func() {
		_ = w.Serve(l)
	}()

	// Storage driver
	storageDriver := local.New(os.TempDir())

	client, err := producer.New(producer.Config{
		Scheme:        "inproc",
		Endpoint:      endpoint,
		SecretKey:     secretKey,
		Timeout:       2 * time.Second,
		StorageDriver: storageDriver,
	})
	if err != nil {
		t.Fatalf("producer.New failed: %v", err)
	}

	// 1. Request upload slot
	slot, err := client.RequestUploadSlot(context.Background(), "tenant-a/data.xlsx", 15*time.Minute)
	if err != nil {
		t.Fatalf("RequestUploadSlot failed: %v", err)
	}
	if slot.StorageReference != "tenant-a/data.xlsx" {
		t.Fatalf("unexpected storage ref: %s", slot.StorageReference)
	}

	// 2. Submit task
	task := &protocol.TaskMessage{
		TaskID:       "task-abc-123",
		TenantID:     "tenant-a",
		ActionTarget: "excel-cleaner",
		SourceURI:    slot.TargetURL,
	}

	res, err := client.SubmitTask(context.Background(), task)
	if err != nil {
		t.Fatalf("SubmitTask failed: %v", err)
	}

	if res.TaskID != task.TaskID || !res.IsSuccess || res.ProcessedRows != 5000 {
		t.Fatalf("unexpected task result: %+v", res)
	}
}

func TestProducerClientQueueIntegration(t *testing.T) {
	memDriver := queue.NewMemoryDriver()

	// 1. Client with QueueDriver configured
	client, err := producer.New(producer.Config{
		QueueDriver: memDriver,
	})
	if err != nil {
		t.Fatalf("producer.New with QueueDriver failed: %v", err)
	}

	task := &protocol.TaskMessage{
		TaskID:       "task-queue-1",
		TenantID:     "tenant-acme",
		ActionTarget: "csv-normalizer",
		SourceURI:    "s3://bucket/tenant-acme/file.csv",
	}

	// Submit via direct RPC should fail since no endpoint was set
	_, err = client.SubmitTask(context.Background(), task)
	if err == nil {
		t.Fatal("expected SubmitTask to fail when endpoint not configured")
	}

	// 2. EnqueueTask into driver
	topic := "subzero.tenant-acme.tasks"
	err = client.EnqueueTask(context.Background(), topic, task, queue.WithPriority(queue.PriorityHigh))
	if err != nil {
		t.Fatalf("EnqueueTask failed: %v", err)
	}

	// 3. Dequeue and verify
	deliveries, err := memDriver.Dequeue(context.Background(), queue.DequeueRequest{
		BatchSize: 10,
	})
	if err != nil {
		t.Fatalf("Dequeue failed: %v", err)
	}
	if len(deliveries) != 1 {
		t.Fatalf("expected 1 delivery, got %d", len(deliveries))
	}

	var dequeuedTask protocol.TaskMessage
	if err := json.Unmarshal(deliveries[0].Message.Payload, &dequeuedTask); err != nil {
		t.Fatalf("unmarshal payload failed: %v", err)
	}

	if dequeuedTask.TaskID != task.TaskID || dequeuedTask.TenantID != task.TenantID {
		t.Fatalf("unexpected dequeued task: %+v", dequeuedTask)
	}

	// 4. Test EnqueueWithDriver with separate driver
	separateDriver := queue.NewMemoryDriver()
	err = client.EnqueueWithDriver(context.Background(), separateDriver, "topic-separate", task)
	if err != nil {
		t.Fatalf("EnqueueWithDriver failed: %v", err)
	}

	deliveries2, err := separateDriver.Dequeue(context.Background(), queue.DequeueRequest{
		BatchSize: 5,
	})
	if err != nil || len(deliveries2) != 1 {
		t.Fatalf("expected 1 delivery from separate driver, err: %v, len: %d", err, len(deliveries2))
	}

	// 5. Test error validation
	if err := client.EnqueueTask(context.Background(), "", task); err == nil {
		t.Fatal("expected error on empty topic")
	}
	if err := client.EnqueueTask(context.Background(), "topic", nil); err == nil {
		t.Fatal("expected error on nil task")
	}
}
