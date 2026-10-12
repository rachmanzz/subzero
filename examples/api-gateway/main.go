package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/rachmanzz/subzero/internal/storage/local"
	"github.com/rachmanzz/subzero/pkg/producer"
	"github.com/rachmanzz/subzero/pkg/protocol"
	_ "github.com/rachmanzz/subzero/pkg/transport/http"
	_ "github.com/rachmanzz/subzero/pkg/transport/inproc"
	_ "github.com/rachmanzz/subzero/pkg/transport/ipc"
	_ "github.com/rachmanzz/subzero/pkg/transport/tcp"
)

// AppServer demonstrates an enterprise B2B SaaS Main API Gateway integrating Subzero.
type AppServer struct {
	client *producer.Client
}

type RequestUploadPayload struct {
	TenantID string `json:"tenant_id"`
	Filename string `json:"filename"`
}

type ProcessPayload struct {
	TenantID     string            `json:"tenant_id"`
	ActionTarget string            `json:"action_target"`
	SourceURI    string            `json:"source_uri"`
	Config       map[string]string `json:"config,omitempty"`
}

func main() {
	port := flag.Int("port", 3000, "Port for the Main API Gateway")
	supervisorScheme := flag.String("supervisor-scheme", "unix", "Transport scheme to Subzero Supervisor")
	supervisorAddr := flag.String("supervisor-addr", "/tmp/subzero-supervisor.sock", "Address of Subzero Supervisor")
	secretKey := flag.String("secret", "subzero-secret-key-32b-default!", "HMAC secret key")
	flag.Parse()

	// Initialize Storage Driver (Local for demo, or S3/MinIO for cloud)
	storageDir := filepath.Join(os.TempDir(), "subzero-storage-demo")
	_ = os.MkdirAll(storageDir, 0755)
	storageDriver := local.New(storageDir)

	// Initialize Subzero Producer Client SDK
	producerClient, err := producer.New(producer.Config{
		Scheme:        *supervisorScheme,
		Endpoint:      *supervisorAddr,
		SecretKey:     []byte(*secretKey),
		Timeout:       10 * time.Second,
		StorageDriver: storageDriver,
	})
	if err != nil {
		log.Fatalf("[Main API] Failed to connect to Subzero: %v", err)
	}

	app := &AppServer{client: producerClient}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/documents/request-upload", app.handleRequestUpload)
	mux.HandleFunc("POST /api/v1/documents/process", app.handleProcessDocument)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	log.Printf("[Main API Gateway] Listening on http://localhost:%d", *port)
	log.Printf("[Main API Gateway] Connected to Subzero Supervisor at %s://%s", *supervisorScheme, *supervisorAddr)

	if err := http.ListenAndServe(fmt.Sprintf(":%d", *port), mux); err != nil {
		log.Fatalf("[Main API] Server terminated: %v", err)
	}
}

// 1. Request Upload Slot: Client requests a pre-signed URL to upload file directly to storage.
func (app *AppServer) handleRequestUpload(w http.ResponseWriter, r *http.Request) {
	var payload RequestUploadPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid JSON payload", http.StatusBadRequest)
		return
	}

	if payload.TenantID == "" || payload.Filename == "" {
		http.Error(w, "tenant_id and filename are required", http.StatusBadRequest)
		return
	}

	storageRef := fmt.Sprintf("%s/%d-%s", payload.TenantID, time.Now().Unix(), payload.Filename)

	// Request upload slot from Subzero storage driver
	slot, err := app.client.RequestUploadSlot(r.Context(), storageRef, 15*time.Minute)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to allocate upload slot: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(slot)
}

// 2. Process Document: Client triggers workload transformation once file upload is complete.
func (app *AppServer) handleProcessDocument(w http.ResponseWriter, r *http.Request) {
	var payload ProcessPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid JSON payload", http.StatusBadRequest)
		return
	}

	taskID := fmt.Sprintf("task-%d", time.Now().UnixNano())
	task := &protocol.TaskMessage{
		TaskID:       taskID,
		TenantID:     payload.TenantID,
		ActionTarget: payload.ActionTarget,
		SourceURI:    payload.SourceURI,
		CustomConfig: payload.Config,
	}

	log.Printf("[Main API] Dispatching task %s to Subzero Supervisor...", taskID)

	// Submit task via Subzero Producer Client SDK
	result, err := app.client.SubmitTask(r.Context(), task)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}
