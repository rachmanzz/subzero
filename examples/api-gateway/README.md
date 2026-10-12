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

### 3. Request an Upload Slot
In terminal 3:
```bash
curl -X POST http://localhost:3000/api/v1/documents/request-upload \
  -H "Content-Type: application/json" \
  -d '{"tenant_id": "tenant-acme", "filename": "revenue.csv"}'
```
Response:
```json
{
  "target_url": "file:///tmp/subzero-storage-demo/tenant-acme/1791778100-revenue.csv",
  "http_method": "PUT",
  "storage_reference": "tenant-acme/1791778100-revenue.csv",
  "expired_at": "2026-10-12T11:22:00Z"
}
```

### 4. Trigger Document Processing
```bash
curl -X POST http://localhost:3000/api/v1/documents/process \
  -H "Content-Type: application/json" \
  -d '{
    "tenant_id": "tenant-acme",
    "action_target": "csv-validator",
    "source_uri": "file:///tmp/subzero-storage-demo/tenant-acme/1791778100-revenue.csv"
  }'
```
Response:
```json
{
  "task_id": "task-1791778120300",
  "is_success": true,
  "processed_rows": 100,
  "error_message": "",
  "execution_time": "12.4ms"
}
```
Notice that Subzero cold-started the worker on demand, executed the workload, and released memory when idle!
