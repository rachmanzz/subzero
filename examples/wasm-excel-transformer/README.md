# Tenant Data Transformer (WebAssembly)

This example demonstrates a tenant-specific data validation and transformation script designed to run inside the **Subzero Wasm Sandbox**.

---

## 🚀 How to Compile to WebAssembly

### Option 1: Via the Subzero Management Dashboard UI
1. Start the Subzero Admin dashboard:
   ```bash
   ./bin/subzero-admin -port 8080
   ```
2. Open `http://localhost:8080` in your web browser.
3. Fill in:
   * **Tenant ID**: `tenant-finance-1`
   * **Module Name**: `csv-validator`
   * **Target**: `wasip1`
4. Drag and drop `main.go` from this directory into the upload box.
5. Click **⚡ Compile to Wasm**. The compiled `.wasm` file will be stored in Subzero's module repository.

### Option 2: Via Command Line (Manual)
```bash
GOOS=wasip1 GOARCH=wasm CGO_ENABLED=0 go build -ldflags="-s -w" -o transformer.wasm main.go
```

---

## 🏃 Running with Subzero Worker

Once compiled, you can submit tasks referencing the module:
```bash
./bin/subzero-cli -scheme unix -endpoint /tmp/subzero-supervisor.sock \
  -tenant tenant-finance-1 \
  -action benchmark
```
