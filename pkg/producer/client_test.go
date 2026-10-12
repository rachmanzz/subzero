package producer_test

import (
	"context"
	"os"
	"testing"
	"time"

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
