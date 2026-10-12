package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rachmanzz/subzero/internal/storage"
	"github.com/rachmanzz/subzero/internal/storage/local"
	"github.com/rachmanzz/subzero/internal/storage/s3"
	"github.com/rachmanzz/subzero/pkg/producer"
	"github.com/rachmanzz/subzero/pkg/protocol"
	_ "github.com/rachmanzz/subzero/pkg/transport/http"
	_ "github.com/rachmanzz/subzero/pkg/transport/inproc"
	_ "github.com/rachmanzz/subzero/pkg/transport/ipc"
	_ "github.com/rachmanzz/subzero/pkg/transport/tcp"
)

// AppServer demonstrates an enterprise B2B SaaS Main API Gateway integrating Subzero (PRD 1.1, 4, 5.3).
type AppServer struct {
	client        *producer.Client
	storageDriver storage.Driver
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
	supervisorScheme := flag.String("supervisor-scheme", "tcp", "Transport scheme to Subzero Supervisor (tcp or unix)")
	supervisorAddr := flag.String("supervisor-addr", "localhost:9090", "Address of Subzero Supervisor")
	secretKey := flag.String("secret", "subzero-secret-key-32b-default!", "HMAC secret key")
	storageScheme := flag.String("storage-scheme", "local", "Storage scheme: local or s3")
	s3Endpoint := flag.String("s3-endpoint", "http://localhost:9000", "MinIO / S3 endpoint")
	s3Bucket := flag.String("s3-bucket", "tenant-vault", "S3 bucket name")
	s3AccessKey := flag.String("s3-access-key", "minioadmin", "S3 Access Key")
	s3SecretKey := flag.String("s3-secret-key", "minioadmin", "S3 Secret Key")
	flag.Parse()

	// Initialize Storage Driver
	var storageDriver storage.Driver
	if *storageScheme == "s3" {
		var err error
		storageDriver, err = s3.New(s3.Config{
			Bucket:       *s3Bucket,
			Region:       "us-east-1",
			Endpoint:     *s3Endpoint,
			AccessKey:    *s3AccessKey,
			SecretKey:    *s3SecretKey,
			UsePathStyle: true,
		})
		if err != nil {
			log.Fatalf("[Main API] Failed to initialize S3 storage driver: %v", err)
		}
		log.Printf("[Main API] S3 Storage configured: %s (bucket: %s)", *s3Endpoint, *s3Bucket)
	} else {
		storageDir := filepath.Join(os.TempDir(), "subzero-storage-demo")
		_ = os.MkdirAll(storageDir, 0755)
		storageDriver = local.New(storageDir)
		log.Printf("[Main API] Local Storage configured: %s", storageDir)
	}

	// Initialize Subzero Producer Client SDK
	producerClient, err := producer.New(producer.Config{
		Scheme:        *supervisorScheme,
		Endpoint:      *supervisorAddr,
		SecretKey:     []byte(*secretKey),
		Timeout:       15 * time.Second,
		StorageDriver: storageDriver,
	})
	if err != nil {
		log.Fatalf("[Main API] Failed to connect to Subzero: %v", err)
	}

	app := &AppServer{
		client:        producerClient,
		storageDriver: storageDriver,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", app.handleHomeUI)
	mux.HandleFunc("POST /api/v1/documents/request-upload", app.handleRequestUpload)
	mux.HandleFunc("POST /api/v1/documents/process", app.handleProcessDocument)
	mux.HandleFunc("PUT /api/v1/storage/upload", app.handleLocalUploadProxy)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"online","gateway":"subzero-b2b"}`))
	})

	log.Printf("[Main API Gateway] Interactive Web Portal ready at http://localhost:%d", *port)
	log.Printf("[Main API Gateway] Subzero Supervisor connection target: %s://%s", *supervisorScheme, *supervisorAddr)

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

	cleanFilename := filepath.Base(payload.Filename)
	storageRef := fmt.Sprintf("%s/%d-%s", payload.TenantID, time.Now().Unix(), cleanFilename)
	if app.storageDriver.Scheme() == "s3" {
		storageRef = fmt.Sprintf("s3://tenant-vault/%s", storageRef)
	}

	// Request upload slot from Subzero storage driver
	slot, err := app.client.RequestUploadSlot(r.Context(), storageRef, 15*time.Minute)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to allocate upload slot: %v", err), http.StatusInternalServerError)
		return
	}

	// If local file scheme, map to local upload proxy endpoint
	if strings.HasPrefix(slot.TargetURL, "file://") {
		slot.TargetURL = fmt.Sprintf("/api/v1/storage/upload?ref=%s", storageRef)
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

	log.Printf("[Main API] Dispatching task %s to Subzero Supervisor (target: %s)...", taskID, payload.ActionTarget)

	// Submit task via Subzero Producer Client SDK
	start := time.Now()
	result, err := app.client.SubmitTask(r.Context(), task)
	duration := time.Since(start)

	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	resp := map[string]any{
		"task_id":        result.TaskID,
		"success":        result.IsSuccess,
		"processed_rows": result.ProcessedRows,
		"execution_time": result.ExecutionTime,
		"roundtrip_ms":   duration.Milliseconds(),
		"error_message":  result.ErrorMessage,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// Local storage upload proxy for local demo fallback
func (app *AppServer) handleLocalUploadProxy(w http.ResponseWriter, r *http.Request) {
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		http.Error(w, "missing ref query param", http.StatusBadRequest)
		return
	}
	err := app.storageDriver.PutObject(r.Context(), ref, r.Body, r.ContentLength)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "OK")
}

// Interactive Web Portal UI
func (app *AppServer) handleHomeUI(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, homeHTML)
}

const homeHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <title>Subzero B2B SaaS API Gateway Demo</title>
  <style>
    :root {
      --bg: #090d16; --card: #131b2e; --accent: #38bdf8; --border: #1e293b;
      --text: #f8fafc; --muted: #94a3b8; --success: #34d399; --danger: #f87171;
    }
    body {
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
      background: var(--bg); color: var(--text); margin: 0; padding: 40px 20px;
      display: flex; justify-content: center;
    }
    .container { width: 100%; max-width: 800px; }
    .header { text-align: center; margin-bottom: 30px; }
    h1 { color: var(--accent); margin: 0 0 10px; font-size: 28px; }
    p.sub { color: var(--muted); margin: 0; font-size: 15px; }
    .card {
      background: var(--card); border: 1px solid var(--border);
      border-radius: 12px; padding: 28px; margin-bottom: 24px;
      box-shadow: 0 10px 25px rgba(0,0,0,0.3);
    }
    .form-group { margin-bottom: 18px; }
    label { display: block; font-size: 13px; font-weight: 600; color: var(--muted); margin-bottom: 6px; text-transform: uppercase; letter-spacing: 0.5px; }
    input, select {
      width: 100%; box-sizing: border-box; background: #0b1120; border: 1px solid var(--border);
      border-radius: 8px; color: var(--text); padding: 12px 14px; font-size: 15px;
    }
    input:focus, select:focus { outline: none; border-color: var(--accent); }
    .btn {
      display: block; width: 100%; background: linear-gradient(135deg, #0284c7, #0ea5e9);
      color: #fff; font-weight: 700; font-size: 16px; padding: 14px; border: none;
      border-radius: 8px; cursor: pointer; transition: all 0.2s;
    }
    .btn:hover { background: linear-gradient(135deg, #0369a1, #0284c7); transform: translateY(-1px); }
    .btn:disabled { opacity: 0.5; cursor: not-allowed; }
    .steps { margin-top: 24px; font-size: 14px; line-height: 1.8; }
    .step-item { display: flex; align-items: center; margin-bottom: 8px; color: var(--muted); }
    .step-item.active { color: var(--accent); font-weight: 600; }
    .step-item.done { color: var(--success); }
    .badge {
      display: inline-block; width: 20px; height: 20px; border-radius: 50%;
      background: #1e293b; color: #fff; text-align: center; line-height: 20px;
      font-size: 11px; margin-right: 10px;
    }
    .step-item.done .badge { background: var(--success); }
    .step-item.active .badge { background: var(--accent); }
    .result-box {
      margin-top: 20px; padding: 16px; border-radius: 8px; background: #0b1120;
      border: 1px solid var(--border); font-family: monospace; font-size: 13px;
      white-space: pre-wrap; display: none;
    }
  </style>
</head>
<body>
  <div class="container">
    <div class="header">
      <h1>❄️ Subzero B2B SaaS Gateway</h1>
      <p class="sub">Direct Storage Ingestion & Sandboxed Scale-to-Zero Computation (PRD 4 & 5.3)</p>
    </div>

    <div class="card">
      <form id="uploadForm">
        <div class="form-group">
          <label>Tenant ID (Company Identifier)</label>
          <input type="text" id="tenantId" value="acme-corp" required>
        </div>

        <div class="form-group">
          <label>Action Target / Transformer Module</label>
          <input type="text" id="actionTarget" value="excel-normalizer" required>
        </div>

        <div class="form-group">
          <label>Select Spreadsheet Document (CSV / Excel)</label>
          <input type="file" id="docFile" accept=".csv,.xlsx" required>
        </div>

        <button type="submit" id="submitBtn" class="btn">🚀 Upload & Normalize Document</button>
      </form>

      <div class="steps" id="stepsContainer" style="display:none;">
        <div class="step-item" id="step1"><span class="badge">1</span> Requesting Ephemeral Upload Slot (Presigned URL)...</div>
        <div class="step-item" id="step2"><span class="badge">2</span> Direct Ingestion to Storage (Zero payload in control plane)...</div>
        <div class="step-item" id="step3"><span class="badge">3</span> Dispatching to Subzero Supervisor (Cold-start -> Sandboxed Wasm -> Scale-to-zero)...</div>
      </div>

      <div class="result-box" id="resultBox"></div>
    </div>
  </div>

  <script>
    const form = document.getElementById('uploadForm');
    const submitBtn = document.getElementById('submitBtn');
    const stepsContainer = document.getElementById('stepsContainer');
    const resultBox = document.getElementById('resultBox');

    form.addEventListener('submit', async (e) => {
      e.preventDefault();
      const tenantId = document.getElementById('tenantId').value.trim();
      const actionTarget = document.getElementById('actionTarget').value.trim();
      const fileInput = document.getElementById('docFile');
      const file = fileInput.files[0];
      if (!file) return;

      submitBtn.disabled = true;
      stepsContainer.style.display = 'block';
      resultBox.style.display = 'none';

      const s1 = document.getElementById('step1');
      const s2 = document.getElementById('step2');
      const s3 = document.getElementById('step3');
      [s1, s2, s3].forEach(el => { el.className = 'step-item'; });

      try {
        // Step 1: Request Ephemeral Presigned Upload Slot
        s1.className = 'step-item active';
        const slotRes = await fetch('/api/v1/documents/request-upload', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ tenant_id: tenantId, filename: file.name })
        });
        if (!slotRes.ok) throw new Error(await slotRes.text());
        const slot = await slotRes.json();
        s1.className = 'step-item done';

        // Step 2: Direct Binary Upload to Storage Driver
        s2.className = 'step-item active';
        const putRes = await fetch(slot.target_url, {
          method: slot.http_method || 'PUT',
          body: file
        });
        if (!putRes.ok) throw new Error('Direct upload to storage failed');
        s2.className = 'step-item done';

        // Step 3: Trigger Execution via Subzero Supervisor
        s3.className = 'step-item active';
        const procRes = await fetch('/api/v1/documents/process', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            tenant_id: tenantId,
            action_target: actionTarget,
            source_uri: slot.storage_reference
          })
        });
        if (!procRes.ok) throw new Error(await procRes.text());
        const result = await procRes.json();
        s3.className = 'step-item done';

        // Display results
        resultBox.style.display = 'block';
        resultBox.innerHTML = JSON.stringify(result, null, 2);
      } catch (err) {
        resultBox.style.display = 'block';
        resultBox.style.color = '#f87171';
        resultBox.textContent = '❌ Error: ' + err.message;
      } finally {
        submitBtn.disabled = false;
      }
    });
  </script>
</body>
</html>
`
