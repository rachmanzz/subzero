package main

import (
	"bytes"
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
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

