package main

import (
	"bytes"
	"context"
	"flag"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rachmanzz/subzero/pkg/management"
)

func TestCLI_ModulePackAndAudit(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cli-test-module-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create valid Go transformer with standard structure
	srcCode := `package main
import "fmt"
func main() { fmt.Println("CLI Test Wasm") }`
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte(srcCode), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module climod\ngo 1.22"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "config.zero"), []byte("name: climod\nentrypoint: main.go\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "readme.md"), []byte("# CLI Module Docs\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// 1. Test Audit (Clean)
	err = handleModuleAudit([]string{tmpDir})
	if err != nil {
		t.Fatalf("expected audit to pass, got: %v", err)
	}

	// 2. Test Pack
	outBundle := filepath.Join(tmpDir, "bundle.tar.gz")
	err = handleModulePack([]string{"-o", outBundle, tmpDir})
	if err != nil {
		t.Fatalf("pack failed: %v", err)
	}

	if _, err := os.Stat(outBundle); err != nil {
		t.Fatalf("bundle file not found: %v", err)
	}

	// 3. Test Audit on the generated archive
	err = handleModuleAudit([]string{outBundle})
	if err != nil {
		t.Fatalf("audit on archive failed: %v", err)
	}
}

func TestCLI_ModuleAuditBackdoorDetection(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cli-test-backdoor-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Code with os/exec backdoor
	malCode := `package main
import "os/exec"
func main() { exec.Command("whoami") }`
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte(malCode), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "config.zero"), []byte("name: backdoor-mod\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "readme.md"), []byte("# Malicious\n"), 0644); err != nil {
		t.Fatal(err)
	}

	err = handleModuleAudit([]string{tmpDir})
	if err == nil {
		t.Fatal("expected audit to fail for backdoor code, got nil")
	}

	// Pack should also fail due to pre-flight audit
	err = handleModulePack([]string{tmpDir})
	if err == nil {
		t.Fatal("expected pack to fail due to pre-flight audit, got nil")
	}
}

func TestCLI_ModulePublishListDownloadE2E(t *testing.T) {
	dataDir, err := os.MkdirTemp("", "cli-test-store-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dataDir)

	store, err := management.NewFileStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}

	srv, err := management.NewServer(management.ServerConfig{
		Compiler: management.NewGoCompiler(""),
		Store:    store,
	})
	if err != nil {
		t.Fatal(err)
	}

	httpSrv := httptest.NewServer(srv.Routes())
	defer httpSrv.Close()

	// Create valid module
	modDir, err := os.MkdirTemp("", "cli-test-publish-mod-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(modDir)

	validCode := `package main
import "fmt"
func main() { fmt.Println("Published via CLI") }`
	_ = os.WriteFile(filepath.Join(modDir, "main.go"), []byte(validCode), 0644)
	_ = os.WriteFile(filepath.Join(modDir, "go.mod"), []byte("module testmod\ngo 1.22"), 0644)
	_ = os.WriteFile(filepath.Join(modDir, "config.zero"), []byte("name: currency-normalizer\ntenant: acme-corp\nentrypoint: main.go\n"), 0644)
	_ = os.WriteFile(filepath.Join(modDir, "readme.md"), []byte("# Currency Normalizer\nNormalizes currencies.\n"), 0644)

	// 1. Test Publish from Directory
	err = handleModulePublish([]string{
		"--server", httpSrv.URL,
		"--tenant", "acme-corp",
		"--name", "currency-normalizer",
		modDir,
	})
	if err != nil {
		t.Fatalf("publish failed: %v", err)
	}

	// 2. Test List
	err = handleModuleList([]string{
		"--server", httpSrv.URL,
		"--tenant", "acme-corp",
	})
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}

	// 3. Query module ID from store to test download
	modules, err := store.List(context.Background(), "acme-corp")
	if err != nil || len(modules) != 1 {
		t.Fatalf("expected 1 module in store, got %d (err: %v)", len(modules), err)
	}
	modID := modules[0].ID

	outWasm := filepath.Join(modDir, "downloaded.wasm")
	err = handleModuleDownload([]string{
		"--server", httpSrv.URL,
		"-o", outWasm,
		modID,
	})
	if err != nil {
		t.Fatalf("download failed: %v", err)
	}

	wasmBytes, err := os.ReadFile(outWasm)
	if err != nil || len(wasmBytes) == 0 {
		t.Fatalf("downloaded wasm is empty or missing: %v", err)
	}

	// Verify Wasm magic header (\x00asm)
	if !bytes.HasPrefix(wasmBytes, []byte("\x00asm")) {
		t.Fatal("downloaded file is not a valid WebAssembly binary")
	}
}

func TestCLI_ModuleInit(t *testing.T) {
	initDir, err := os.MkdirTemp("", "cli-test-init-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(initDir)

	subDir := filepath.Join(initDir, "new-mod")
	err = handleModuleInit([]string{
		"--name", "custom-filter",
		"--tenant", "tenant-x",
		subDir,
	})
	if err != nil {
		t.Fatalf("handleModuleInit failed: %v", err)
	}

	// Verify generated files
	for _, expectedFile := range []string{"config.zero", "readme.md", "main.go", "go.mod"} {
		p := filepath.Join(subDir, expectedFile)
		if fi, err := os.Stat(p); err != nil || fi.Size() == 0 {
			t.Fatalf("expected non-empty file %s, err: %v", expectedFile, err)
		}
	}

	// Verify structure validation passes on initialized module
	pkgInfo, err := management.VerifyModulePackage(subDir)
	if err != nil {
		t.Fatalf("VerifyModulePackage failed on scaffolded module: %v", err)
	}
	if pkgInfo.Config.Name != "custom-filter" || pkgInfo.Config.Tenant != "tenant-x" {
		t.Fatalf("unexpected config values in scaffolded module: %+v", pkgInfo.Config)
	}
}

func TestCLI_ModuleInspectPolicy(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cli-test-inspect-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Valid module
	validModDir := filepath.Join(tmpDir, "valid-mod")
	_ = os.MkdirAll(validModDir, 0755)
	_ = os.WriteFile(filepath.Join(validModDir, "main.go"), []byte("package main\nfunc main() {}\n"), 0644)
	_ = os.WriteFile(filepath.Join(validModDir, "go.mod"), []byte("module validmod\ngo 1.22\n"), 0644)
	_ = os.WriteFile(filepath.Join(validModDir, "readme.md"), []byte("# Valid Mod\n"), 0644)
	cfgValid := `name: valid-mod
permissions.host_calls: storage:get_object
permissions.filesystem.read_only: /tmp/subzero-tenants/in
permissions.files.allowed_extensions: .csv
`
	_ = os.WriteFile(filepath.Join(validModDir, "config.zero"), []byte(cfgValid), 0644)

	// Test 1: Inspect valid module with default policy (offline / local fallback)
	err = handleModuleInspectPolicy([]string{
		"-module", validModDir,
		"-server", "http://127.0.0.1:1", // intentionally unreachable server to test local fallback
	})
	if err != nil {
		t.Fatalf("expected module inspection to succeed, got: %v", err)
	}

	// Test 2: Inspect module with forbidden host calls against custom policy
	policyFile := filepath.Join(tmpDir, "strict_policy.json")
	strictPolicy := `{
		"allowed_host_calls": ["logger:info"],
		"filesystem": { "allowed_read_only_roots": ["/data"] }
	}`
	_ = os.WriteFile(policyFile, []byte(strictPolicy), 0644)

	err = handleModuleInspectPolicy([]string{
		"-module", validModDir,
		"-policy", policyFile,
		"-server", "http://127.0.0.1:1",
	})
	if err == nil {
		t.Fatal("expected inspection failure due to forbidden host call, got nil")
	}

	// Test 3: Inspect module against live Management API server
	store, err := management.NewFileStore(filepath.Join(tmpDir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	mgmtServer, err := management.NewServer(management.ServerConfig{
		Compiler: management.NewGoCompiler(""),
		Store:    store,
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(mgmtServer.Routes())
	defer ts.Close()

	err = handleModuleInspectPolicy([]string{
		"-module", validModDir,
		"-server", ts.URL,
	})
	if err != nil {
		t.Fatalf("inspection against management server failed: %v", err)
	}
}

func TestCLI_TaskRequestUpload(t *testing.T) {
	// Test 1: Local file upload slot allocation (success with valid TTL)
	err := handleTaskRequestUpload([]string{
		"-ref", "file:///tmp/subzero-test/input.csv",
		"-ttl", "10m",
	})
	if err != nil {
		t.Fatalf("expected request-upload to succeed, got: %v", err)
	}

	// Test 2: S3 upload slot allocation
	err = handleTaskRequestUpload([]string{
		"-ref", "s3://tenant-vault/dataset.csv",
		"-ttl", "15m",
		"-endpoint", "http://localhost:9000",
		"-access-key", "minioadmin",
		"-secret-key", "minioadmin",
	})
	if err != nil {
		t.Fatalf("expected s3 request-upload to succeed, got: %v", err)
	}

	// Test 3: Rejection of TTL exceeding 15 minutes (PRD 7)
	err = handleTaskRequestUpload([]string{
		"-ref", "s3://tenant-vault/dataset.csv",
		"-ttl", "16m",
	})
	if err == nil {
		t.Fatal("expected TTL > 15m to be rejected, got nil")
	}

	// Test 4: Missing storage reference error
	err = handleTaskRequestUpload([]string{
		"-ttl", "5m",
	})
	if err == nil {
		t.Fatal("expected missing -ref error, got nil")
	}
}

func TestCLI_ModuleInitFlags(t *testing.T) {
	tmpDir := t.TempDir()
	modDir := filepath.Join(tmpDir, "new-mod")

	err := handleModuleInit([]string{
		"-name", "custom-filter",
		"-tenant", "tenant-test",
		modDir,
	})
	if err != nil {
		t.Fatalf("handleModuleInit failed: %v", err)
	}

	// Verify generated config.zero, readme.md, main.go
	for _, expectedFile := range []string{"config.zero", "readme.md", "main.go"} {
		p := filepath.Join(modDir, expectedFile)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected generated file %s to exist: %v", p, err)
		}
	}

	cfgContent, _ := os.ReadFile(filepath.Join(modDir, "config.zero"))
	if !strings.Contains(string(cfgContent), "name: custom-filter") {
		t.Fatalf("expected config.zero to contain custom-filter name, got: %s", string(cfgContent))
	}
}

func TestCLI_ParseFlexible(t *testing.T) {
	fs := flag.NewFlagSet("test-flexible", flag.ContinueOnError)
	name := fs.String("name", "", "Name")
	active := fs.Bool("active", false, "Active flag")
	count := fs.Int("count", 0, "Count")

	// Interleaved positional and flag arguments: <pos1> -name <val> <pos2> -active -count 42 <pos3>
	args := []string{"first-dir", "-name", "my-mod", "second-dir", "-active", "-count", "42", "third-dir"}
	positionals, err := parseFlexible(fs, args)
	if err != nil {
		t.Fatalf("parseFlexible failed: %v", err)
	}

	if *name != "my-mod" {
		t.Errorf("expected name 'my-mod', got %q", *name)
	}
	if !*active {
		t.Errorf("expected active to be true")
	}
	if *count != 42 {
		t.Errorf("expected count 42, got %d", *count)
	}

	expectedPos := []string{"first-dir", "second-dir", "third-dir"}
	if len(positionals) != len(expectedPos) {
		t.Fatalf("expected %d positionals, got %d: %+v", len(expectedPos), len(positionals), positionals)
	}
	for i, p := range expectedPos {
		if positionals[i] != p {
			t.Errorf("positional[%d] = %q, want %q", i, positionals[i], p)
		}
	}
}

func TestCLI_HandleModuleCommandDispatch(t *testing.T) {
	// Unknown command
	err := handleModuleCommand([]string{"nonexistent-command"})
	if err == nil {
		t.Fatal("expected error for unknown module subcommand, got nil")
	}

	// Empty args shows help and returns nil
	err = handleModuleCommand([]string{})
	if err != nil {
		t.Fatalf("expected nil for empty args, got: %v", err)
	}
}


