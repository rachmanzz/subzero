package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/rachmanzz/subzero/internal/storage/local"
	"github.com/rachmanzz/subzero/internal/supervisor"
	"github.com/rachmanzz/subzero/pkg/management"
	"github.com/rachmanzz/subzero/pkg/permission"
	"github.com/rachmanzz/subzero/pkg/producer"
	"github.com/rachmanzz/subzero/pkg/protocol"
	"github.com/rachmanzz/subzero/pkg/transport"
	_ "github.com/rachmanzz/subzero/pkg/transport/ipc"
)

var (
	e2eWorkerBinPath string
	e2eWorkerBuildMu sync.Mutex
)

func getE2EWorkerBinary(t testing.TB) string {
	e2eWorkerBuildMu.Lock()
	defer e2eWorkerBuildMu.Unlock()

	if e2eWorkerBinPath != "" {
		return e2eWorkerBinPath
	}

	relBin := filepath.Join("..", "..", "bin", "subzero-worker")
	if _, err := os.Stat(relBin); err == nil {
		e2eWorkerBinPath = relBin
		return e2eWorkerBinPath
	}

	tmpDir, err := os.MkdirTemp("", "subzero-e2e-worker-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	binPath := filepath.Join(tmpDir, "subzero-worker")
	cmd := exec.Command("go", "build", "-o", binPath, "../../cmd/subzero-worker")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build subzero-worker: %v, output: %s", err, string(out))
	}
	e2eWorkerBinPath = binPath
	return e2eWorkerBinPath
}

// TestFullTopologyPipelineEndToEnd verifies the complete 5-step lifecycle from PRD Section 4:
// 1. Client requests upload slot -> Main API allocates pre-signed URL (UploadSlotResponse)
// 2. Direct binary upload to Storage Driver (Zero physical payload in control plane)
// 3. Producer dispatches TaskMessage with only URI reference
// 4. Supervisor on-demand Cold-Start / Provisioning & Concurrency Dispatch
// 5. Worker Sandbox direct stream read from storage & Scale-to-Zero Auto-Termination
func TestFullTopologyPipelineEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	tmpDir := t.TempDir()
	workerBin := getE2EWorkerBinary(t)

	// Setup endpoints & secrets
	secretKey := []byte("subzero-topo-e2e-secret-key-32b!")
	supervisorSock := filepath.Join(tmpDir, "supervisor.sock")
	workerSock := filepath.Join(tmpDir, "worker-spawned.sock")
	storageDir := filepath.Join(tmpDir, "storage")
	_ = os.MkdirAll(storageDir, 0755)

	// Register Storage Driver
	storageDriver := local.New(storageDir)

	// Compile Guest Wasm Transformer Module
	transformerSrc := `package main

import (
	"bytes"
	"fmt"
	"github.com/rachmanzz/subzero/pkg/sdk"
)

func main() {
	req, err := sdk.ReadRequestFromEnv()
	if err != nil {
		fmt.Printf("{\"is_success\":false,\"error_message\":%q}\n", err.Error())
		return
	}

	blob, err := sdk.GetStorageBlob(req.SourceURI)
	if err != nil {
		fmt.Printf("{\"is_success\":false,\"error_message\":%q}\n", err.Error())
		return
	}

	// Count rows (excluding header)
	totalRows := int64(bytes.Count(blob, []byte("\n")))
	if totalRows > 0 {
		totalRows--
	}

	fmt.Printf("{\"is_success\":true,\"processed_rows\":%d}\n", totalRows)
}
`
	compiler := management.NewGoCompiler("")
	compileRes, err := compiler.Compile(ctx, []byte(transformerSrc), management.CompileOptions{
		TenantID:   "tenant-topo",
		ModuleName: "transformer",
		TargetOS:   "wasip1",
		Optimize:   true,
	})
	if err != nil {
		t.Fatalf("failed to compile wasm module: %v", err)
	}

	wasmFile := filepath.Join(tmpDir, "transformer.wasm")
	if err := os.WriteFile(wasmFile, compileRes.WasmBytes, 0644); err != nil {
		t.Fatalf("failed to write wasm file: %v", err)
	}

	// Setup Worker Policy granting access to storageDir
	policy := permission.DefaultWorkerPolicy()
	policy.AllowedHostCalls = []string{"storage:get_object", "logger:info"}
	policy.Filesystem.AllowedReadOnlyRoots = []string{storageDir}
	policy.Files.AllowedExtensions = []string{".csv"}

	// Initialize Supervisor Spawner with short idle timeout for scale-to-zero testing
	idleTimeout := 350 * time.Millisecond
	spawner, err := supervisor.NewProcessSpawner(supervisor.SpawnerConfig{
		WorkerBinary: workerBin,
		Scheme:       "unix",
		Endpoint:     workerSock,
		SecretKey:    secretKey,
		IdleTimeout:  idleTimeout,
		ReadyTimeout: 5 * time.Second,
		WorkerPolicy: &policy,
	})
	if err != nil {
		t.Fatalf("NewProcessSpawner failed: %v", err)
	}
	defer spawner.Stop()

	// Initialize Supervisor Dispatcher
	dispatcher := supervisor.NewDispatcher(supervisor.DispatcherConfig{
		Concurrency: 8,
		QueueSize:   64,
		SubLimit:    4,
	})
	dispatcher.Start()
	defer dispatcher.Stop()

	// Start Supervisor Daemon Listener
	tr, err := transport.Get("unix")
	if err != nil {
		t.Fatalf("transport.Get unix failed: %v", err)
	}

	listener, err := tr.Listen(ctx, supervisorSock)
	if err != nil {
		t.Fatalf("Supervisor Listen failed: %v", err)
	}
	defer listener.Close()

	// Run Supervisor Accept & Relay Loop in background
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				rawPayload, err := protocol.ReadFrame(c, secretKey, 0)
				if err != nil {
					return
				}

				// Ensure worker is running (Cold-Start if DORMANT)
				if err := spawner.EnsureRunning(ctx); err != nil {
					errRes := &protocol.ResultMessage{
						IsSuccess:    false,
						ErrorMessage: fmt.Sprintf("cold-start worker failed: %v", err),
					}
					data, _ := protocol.EncodeResult(errRes)
					_ = protocol.WriteFrame(c, data, secretKey)
					return
				}

				// Relay task to spawned worker
				wConn, err := tr.Dial(ctx, workerSock)
				if err != nil {
					errRes := &protocol.ResultMessage{
						IsSuccess:    false,
						ErrorMessage: fmt.Sprintf("dial worker failed: %v", err),
					}
					data, _ := protocol.EncodeResult(errRes)
					_ = protocol.WriteFrame(c, data, secretKey)
					return
				}
				defer wConn.Close()

				if err := protocol.WriteFrame(wConn, rawPayload, secretKey); err != nil {
					return
				}

				resPayload, err := protocol.ReadFrame(wConn, secretKey, 0)
				if err != nil {
					return
				}

				_ = protocol.WriteFrame(c, resPayload, secretKey)
			}(conn)
		}
	}()

	// Initialize Producer Client
	producerClient, err := producer.New(producer.Config{
		Scheme:        "unix",
		Endpoint:      supervisorSock,
		SecretKey:     secretKey,
		Timeout:       10 * time.Second,
		StorageDriver: storageDriver,
	})
	if err != nil {
		t.Fatalf("producer.New failed: %v", err)
	}

	// -------------------------------------------------------------
	// STEP 1: Request Upload Slot (PRD 4 & 5.3)
	// -------------------------------------------------------------
	docRef := "tenant-topo/transactions_q1.csv"
	slot, err := producerClient.RequestUploadSlot(ctx, docRef, 10*time.Minute)
	if err != nil {
		t.Fatalf("Step 1 failed: RequestUploadSlot error: %v", err)
	}

	if slot.TargetURL == "" || slot.HTTPMethod != "PUT" || slot.IsExpired() {
		t.Fatalf("Step 1 failed: invalid upload slot response: %+v", slot)
	}
	t.Logf("[STEP 1 SUCCESS] Upload Slot Allocated: %s (Expires: %v)", slot.TargetURL, slot.ExpiredAt)

	// -------------------------------------------------------------
	// STEP 2: Direct Binary Upload to Storage (PRD 4 & 5.3)
	// -------------------------------------------------------------
	// Generate 100 rows of CSV data
	var csvBuf bytes.Buffer
	csvBuf.WriteString("id,client,amount,status\n")
	for i := 1; i <= 100; i++ {
		csvBuf.WriteString(fmt.Sprintf("%d,client-%d,150.50,CONFIRMED\n", i, i))
	}
	rawDocBytes := csvBuf.Bytes()

	// Direct upload into storage driver without touching supervisor or producer control plane
	err = storageDriver.PutObject(ctx, docRef, bytes.NewReader(rawDocBytes), int64(len(rawDocBytes)))
	if err != nil {
		t.Fatalf("Step 2 failed: PutObject error: %v", err)
	}
	t.Logf("[STEP 2 SUCCESS] Direct Binary Upload completed (%d bytes written directly to storage)", len(rawDocBytes))

	// -------------------------------------------------------------
	// STEP 3 & 4: Zero Payload Dispatch & Cold-Start Provisioning (PRD 4, 5.1, 8)
	// -------------------------------------------------------------
	// Worker starts DORMANT
	if spawner.State() != supervisor.StateDormant {
		t.Fatalf("expected worker initial state DORMANT, got %v", spawner.State())
	}

	permJSON, _ := json.Marshal(permission.ModuleRequest{
		HostCalls: []string{"storage:get_object"},
		Files: permission.RequestFiles{
			AllowedExtensions: []string{".csv"},
		},
	})

	task := &protocol.TaskMessage{
		TaskID:       "task-topo-e2e-001",
		TenantID:     "tenant-topo",
		ActionTarget: wasmFile,
		SourceURI:    slot.TargetURL,
		CustomConfig: map[string]string{
			"pipeline":    "accounting-normalization",
			"permissions": string(permJSON),
		},
	}

	t.Logf("[STEP 3] Submitting task with Zero Payload in Control Plane (SourceURI: %s)", task.SourceURI)
	start := time.Now()
	result, err := producerClient.SubmitTask(ctx, task)
	if err != nil {
		t.Fatalf("SubmitTask failed: %v", err)
	}
	elapsed := time.Since(start)

	// -------------------------------------------------------------
	// STEP 5: Worker Sandbox Stream Read & Auto-Termination (PRD 4, 5.3, 8)
	// -------------------------------------------------------------
	if !result.IsSuccess {
		t.Fatalf("Step 5 failed: ResultMessage reported failure: %s", result.ErrorMessage)
	}

	if result.ProcessedRows != 100 {
		t.Fatalf("Step 5 failed: expected 100 processed rows, got %d", result.ProcessedRows)
	}
	t.Logf("[STEP 4 & 5 SUCCESS] Processed %d rows in %v (Worker cold-start & stream read succeeded)", result.ProcessedRows, elapsed)

	// Verify worker transitioned to RUNNING while handling task
	if spawner.State() != supervisor.StateRunning {
		t.Fatalf("expected worker state RUNNING after task completion, got %v", spawner.State())
	}

	// Wait for idle watchdog timeout window to elapse -> SCALE-TO-ZERO
	t.Logf("[STEP 5 MONITOR] Waiting for scale-to-zero auto-termination (idle timeout: %v)...", idleTimeout)
	deadline := time.Now().Add(idleTimeout + 800*time.Millisecond)
	scaledToZero := false

	for time.Now().Before(deadline) {
		if spawner.State() == supervisor.StateDormant {
			scaledToZero = true
			break
		}
		time.Sleep(30 * time.Millisecond)
	}

	if !scaledToZero {
		t.Errorf("Scale-to-zero failed: worker remained in state %v after idle timeout", spawner.State())
	} else {
		t.Logf("[STEP 5 COMPLETE] Scale-to-Zero Verified: worker automatically terminated and returned to DORMANT")
	}
}
