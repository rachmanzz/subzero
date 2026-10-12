package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/rachmanzz/subzero/pkg/producer"
	"github.com/rachmanzz/subzero/pkg/protocol"
	_ "github.com/rachmanzz/subzero/pkg/transport/http"
	_ "github.com/rachmanzz/subzero/pkg/transport/inproc"
	_ "github.com/rachmanzz/subzero/pkg/transport/ipc"
	_ "github.com/rachmanzz/subzero/pkg/transport/tcp"
)

func main() {
	scheme := flag.String("scheme", "unix", "Transport scheme (unix, tcp, http)")
	endpoint := flag.String("endpoint", "/tmp/subzero-worker.sock", "Worker socket or host address")
	secretKey := flag.String("secret", "subzero-secret-key-32b-default!", "HMAC secret key")
	tenant := flag.String("tenant", "demo-tenant", "Tenant ID")
	action := flag.String("action", "benchmark", "Action (benchmark | ping)")
	flag.Parse()

	client, err := producer.New(producer.Config{
		Scheme:    *scheme,
		Endpoint:  *endpoint,
		SecretKey: []byte(*secretKey),
		Timeout:   5 * time.Second,
	})
	if err != nil {
		log.Fatalf("[Subzero CLI] Failed to initialize client: %v", err)
	}

	switch *action {
	case "ping", "benchmark":
		task := &protocol.TaskMessage{
			TaskID:       fmt.Sprintf("cli-%d", time.Now().UnixNano()),
			TenantID:     *tenant,
			ActionTarget: "ping-test",
			SourceURI:    "file:///tmp/sample.csv",
		}

		start := time.Now()
		res, err := client.SubmitTask(context.Background(), task)
		latency := time.Since(start)

		if err != nil {
			log.Fatalf("[Subzero CLI] Task execution failed: %v", err)
		}

		fmt.Printf("✓ Task Completed Successfully!\n")
		fmt.Printf("  Task ID        : %s\n", res.TaskID)
		fmt.Printf("  Success        : %t\n", res.IsSuccess)
		fmt.Printf("  Processed Rows : %d\n", res.ProcessedRows)
		fmt.Printf("  Round-Trip     : %v\n", latency)
		fmt.Printf("  Execution Time : %s\n", res.ExecutionTime)
	default:
		log.Fatalf("Unknown action: %s", *action)
	}
}
