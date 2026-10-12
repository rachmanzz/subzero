package management_test

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/rachmanzz/subzero/pkg/management"
	"github.com/rachmanzz/subzero/pkg/permission"
)

func TestManagementServerE2E(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "subzero-mgmt-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := management.NewFileStore(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	comp := management.NewGoCompiler("")

	srv, err := management.NewServer(management.ServerConfig{
		Compiler: comp,
		Store:    store,
	})
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}

	r := srv.Routes()

	// 1. Test Dashboard HTML
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for dashboard, got %d", w.Code)
	}

	// 2. Test Compile Go to Wasm
	goSource := `package main

import "fmt"

func main() {
	fmt.Println("Subzero Wasm Executed")
}
`
	tarGzBytes, err := createTestTarGz(map[string]string{
		"config.zero": `name: test-parser
version: 1.0.0
tenant: test-tenant
target: wasip1
entrypoint: main.go`,
		"readme.md": "# Test Parser\nConverts test data to wasm.",
		"main.go":   goSource,
	})
	if err != nil {
		t.Fatal(err)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("tenant_id", "test-tenant")
	_ = writer.WriteField("module_name", "test-parser")
	_ = writer.WriteField("target_os", "wasip1")

	part, err := writer.CreateFormFile("file", "package.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(tarGzBytes)
	_ = writer.Close()

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/modules/compile", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("compile failed with status %d: %s", w.Code, w.Body.String())
	}

	var compileRes map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &compileRes); err != nil {
		t.Fatalf("failed to decode compile response: %v", err)
	}

	if compileRes["success"] != true {
		t.Fatalf("compile reported not successful: %+v", compileRes)
	}

	moduleID, ok := compileRes["module_id"].(string)
	if !ok || moduleID == "" {
		t.Fatal("expected module_id in response")
	}

	// 3. Test List Modules
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/modules", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for list modules, got %d", w.Code)
	}

	var listRes []*management.WasmModule
	if err := json.Unmarshal(w.Body.Bytes(), &listRes); err != nil {
		t.Fatal(err)
	}
	if len(listRes) != 1 || listRes[0].ID != moduleID {
		t.Fatalf("unexpected modules list: %+v", listRes)
	}

	// 4. Test Download Wasm
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/modules/"+moduleID+"/download", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for download, got %d", w.Code)
	}
	if w.Header().Get("Content-Type") != "application/wasm" {
		t.Fatalf("expected application/wasm content-type, got %s", w.Header().Get("Content-Type"))
	}
	if len(w.Body.Bytes()) == 0 {
		t.Fatal("downloaded wasm binary is empty")
	}

	// 5. Test Delete Module
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/api/modules/"+moduleID, nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204 No Content for delete, got %d", w.Code)
	}
}

func TestManagementServer_TarGzWorkflow(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "subzero-targz-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := management.NewFileStore(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	srv, err := management.NewServer(management.ServerConfig{
		Compiler: management.NewGoCompiler(""),
		Store:    store,
	})
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}

	r := srv.Routes()

	tarGzBytes, err := createTestTarGz(map[string]string{
		"config.zero": `name: normalizer-bundle
version: 1.0.0
tenant: fintech-corp
entrypoint: main.go
target: wasip1`,
		"readme.md": "# Normalizer Bundle\nNormalizes fintech data",
		"main.go": `package main
import "fmt"
func main() { fmt.Println("hello from tar.gz") }`,
		"go.mod": `module testtargz
go 1.22`,
	})
	if err != nil {
		t.Fatalf("failed to create tar.gz: %v", err)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("tenant_id", "fintech-corp")
	_ = writer.WriteField("module_name", "normalizer-bundle")

	part, err := writer.CreateFormFile("file", "package.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(tarGzBytes)
	_ = writer.Close()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/modules/compile", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["published"] != true {
		t.Fatalf("expected published to be true, got %+v", resp)
	}
}

func TestManagementServer_SecurityAuditReject(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "subzero-audit-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := management.NewFileStore(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	srv, err := management.NewServer(management.ServerConfig{
		Compiler: management.NewGoCompiler(""),
		Store:    store,
	})
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}

	r := srv.Routes()

	// Malicious archive with os/exec
	tarGzBytes, err := createTestTarGz(map[string]string{
		"config.zero": `name: backdoor-bundle
version: 1.0.0
tenant: malicious-user
entrypoint: main.go`,
		"readme.md": "# Malicious Bundle\nTest backdoor detection.",
		"main.go": `package main
import "os/exec"
func main() { exec.Command("cat", "/etc/passwd") }`,
	})
	if err != nil {
		t.Fatalf("failed to create tar.gz: %v", err)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("tenant_id", "malicious-user")
	_ = writer.WriteField("module_name", "backdoor-bundle")

	part, err := writer.CreateFormFile("file", "package.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(tarGzBytes)
	_ = writer.Close()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/modules/compile", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 Unprocessable Entity, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["stage"] != "security_audit" {
		t.Fatalf("expected stage to be security_audit, got %v", resp["stage"])
	}
	if resp["success"] != false {
		t.Fatalf("expected success to be false")
	}
}

func TestManagementServer_StructureReject(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "subzero-struct-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := management.NewFileStore(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	srv, err := management.NewServer(management.ServerConfig{
		Compiler: management.NewGoCompiler(""),
		Store:    store,
	})
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}

	r := srv.Routes()

	// 1. Archive missing config.zero
	badTarBytes, err := createTestTarGz(map[string]string{
		"readme.md": "# Docs",
		"main.go":   "package main\nfunc main() {}",
	})
	if err != nil {
		t.Fatal(err)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("tenant_id", "test-tenant")
	_ = writer.WriteField("module_name", "missing-config")

	part, err := writer.CreateFormFile("file", "package.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(badTarBytes)
	_ = writer.Close()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/modules/compile", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 Unprocessable Entity for missing config.zero, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["stage"] != "structure_verification" {
		t.Fatalf("expected stage structure_verification, got %v", resp["stage"])
	}

	// 2. Reject direct raw .go file
	var body2 bytes.Buffer
	writer2 := multipart.NewWriter(&body2)
	part2, err := writer2.CreateFormFile("file", "main.go")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part2.Write([]byte("package main\nfunc main() {}"))
	_ = writer2.Close()

	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/api/modules/compile", &body2)
	req2.Header.Set("Content-Type", writer2.FormDataContentType())
	r.ServeHTTP(w2, req2)

	if w2.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for direct .go upload, got %d", w2.Code)
	}
}

func TestManagementServer_InspectPolicyDirect(t *testing.T) {
	srv, err := management.NewServer(management.ServerConfig{})
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}
	r := srv.Routes()

	// 1. Valid request within policy
	policy := permission.DefaultWorkerPolicy()
	policy.AllowedHostCalls = []string{"storage:get_object", "logger:info"}
	policy.S3.AllowedPrefixes = []string{"s3://vault-alpha/"}

	reqPayload := management.InspectPolicyRequest{
		WorkerPolicy: &policy,
		ModuleRequest: &permission.ModuleRequest{
			HostCalls: []string{"storage:get_object"},
			S3: permission.RequestS3{
				AllowedPrefixes: []string{"s3://vault-alpha/2026/"},
			},
		},
	}

	bodyBytes, _ := json.Marshal(reqPayload)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/inspect-policy", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	var resp management.InspectPolicyResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response failed: %v", err)
	}

	if !resp.Allowed {
		t.Fatalf("expected allowed true, got false, error: %s", resp.Error)
	}
	if !resp.EffectivePermissions.HostCalls["storage:get_object"] {
		t.Errorf("expected storage:get_object in effective host calls")
	}

	// 2. Privilege Escalation Request (Unauthorized host call)
	escalatedPayload := management.InspectPolicyRequest{
		WorkerPolicy: &policy,
		ModuleRequest: &permission.ModuleRequest{
			HostCalls: []string{"unauthorized:kernel_exec"},
		},
	}
	bodyBytes, _ = json.Marshal(escalatedPayload)
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/inspect-policy", bytes.NewReader(bodyBytes))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", w.Code)
	}

	var escResp management.InspectPolicyResponse
	if err := json.Unmarshal(w.Body.Bytes(), &escResp); err != nil {
		t.Fatalf("unmarshal response failed: %v", err)
	}

	if escResp.Allowed {
		t.Errorf("expected privilege escalation to be rejected (allowed=false)")
	}
	if escResp.Error == "" {
		t.Errorf("expected error message describing violation")
	}
}

func TestManagementServer_InspectModulePolicy(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := management.NewFileStore(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	srv, err := management.NewServer(management.ServerConfig{Store: store})
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}
	r := srv.Routes()

	// Save a module with declared permissions in config.zero
	mod := &management.WasmModule{
		ID:       "mod-inspect-1",
		TenantID: "tenant-beta",
		Name:     "beta-parser",
		Config: &management.ConfigZero{
			Permissions: &permission.ModuleRequest{
				HostCalls: []string{"storage:get_object"},
			},
		},
	}
	if err := store.Save(context.Background(), mod, []byte{0x00, 0x61, 0x73, 0x6d}, nil); err != nil {
		t.Fatal(err)
	}

	// Inspect existing module policy against strict policy
	policy := permission.DefaultWorkerPolicy()
	policy.AllowedHostCalls = []string{"storage:get_object"}

	bodyBytes, _ := json.Marshal(management.InspectPolicyRequest{
		WorkerPolicy: &policy,
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/modules/mod-inspect-1/inspect-policy", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	var resp management.InspectPolicyResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if !resp.Allowed {
		t.Fatalf("expected allowed true, got false: %s", resp.Error)
	}
	if resp.ModuleID != "mod-inspect-1" || resp.TenantID != "tenant-beta" {
		t.Errorf("unexpected module or tenant ID in response: %+v", resp)
	}

	// Test nonexistent module returns 404
	w404 := httptest.NewRecorder()
	req404 := httptest.NewRequest(http.MethodPost, "/api/modules/nonexistent-mod/inspect-policy", nil)
	r.ServeHTTP(w404, req404)

	if w404.Code != http.StatusNotFound {
		t.Errorf("expected 404 for nonexistent module, got %d", w404.Code)
	}
}


