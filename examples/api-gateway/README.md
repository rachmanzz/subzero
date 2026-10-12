# Subzero API Gateway Example

This example demonstrates how an enterprise B2B SaaS API Gateway integrates the **Subzero Producer SDK** ([`pkg/producer`](../../pkg/producer)).

---

## 🏗️ Architecture Flow

```text
[ Web Frontend / Client ]
           │
           ├── 1. POST /api/v1/documents/request-upload ──► [ Main API Gateway ]
           │                                                        │
           │◄── 2. Returns Pre-signed Upload URL ───────────────────┘
           │
           ├── 3. PUT file directly to Storage ──────────► [ Cloud Object Storage ]
           │
           ├── 4. POST /api/v1/documents/process ─────────► [ Main API Gateway ]
           │                                                        │
           │                                                        ▼ (Subzero Producer SDK)
           │                                              [ Subzero Supervisor ]
           │                                                        │
           │                                                        ▼ (Auto Cold-Start)
           │                                              [ Subzero Worker Sandbox ]
           │                                                        │ (Stream parse S3 file)
           │◄── 5. Returns Processed Rows & Execution Metrics ──────┘
```

---

## 🚀 Running the Example

### 1. Start the Subzero Supervisor Daemon
In terminal 1:
```bash
./bin/subzero-supervisor -scheme unix -endpoint /tmp/subzero-supervisor.sock
```

### 2. Start the Main API Gateway
In terminal 2:
```bash
go run ./examples/api-gateway/main.go -port 3000
```

### 3. Open the Interactive Web Portal
Open your web browser at:
```
http://localhost:3000
```
From the web portal:
1. Enter your Tenant ID (e.g., `acme-corp`) and Action Target (`excel-normalizer`).
2. Pick any `.csv` or `.xlsx` document.
3. Click **🚀 Upload & Normalize Document**.
4. The web portal automatically:
   - Allocates an ephemeral pre-signed upload URL.
   - Streams the binary file directly to storage (Zero payload through API gateway).
   - Dispatches `TaskMessage` to Subzero Supervisor.
   - Subzero wakes up the worker (cold-start), processes the spreadsheet in sandboxed Wasm, and auto-terminates back to zero.
   - Displays real-time metrics and processed row counts in the UI!

### 4. Running via Docker Compose
To run the entire Subzero ecosystem (MinIO + Subzero Admin + Subzero Supervisor + API Gateway) in one command:
```bash
make up
```
Once up:
* Subzero Management Dashboard: `http://localhost:8080`
* MinIO Storage Console: `http://localhost:9001` (user: `minioadmin`, pass: `minioadmin`)
* Interactive API Gateway Demo: `http://localhost:3000`

