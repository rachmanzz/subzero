package management_test

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/rachmanzz/subzero/pkg/management"
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
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("tenant_id", "test-tenant")
	_ = writer.WriteField("module_name", "test-parser")
	_ = writer.WriteField("target_os", "wasip1")

	part, err := writer.CreateFormFile("file", "main.go")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte(goSource))
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
