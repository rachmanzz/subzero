package management_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rachmanzz/subzero/pkg/management"
)

func createTestPkgDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "validator-test-*")
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		fullPath := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestVerifyStructure_Valid(t *testing.T) {
	dir := createTestPkgDir(t, map[string]string{
		"config.zero": `name: my-parser
version: 1.0.0
entrypoint: main.go
target: wasip1`,
		"readme.md": "# Parser Documentation\nNormalizes data.",
		"main.go": `package main

import "fmt"

func main() {
	fmt.Println("ok")
}
`,
	})
	defer os.RemoveAll(dir)

	pkgInfo, err := management.VerifyModulePackage(dir)
	if err != nil {
		t.Fatalf("expected valid structure, got: %v", err)
	}
	if filepath.Base(pkgInfo.MainFile) != "main.go" {
		t.Errorf("expected main.go, got %s", pkgInfo.MainFile)
	}
	if pkgInfo.Config.Name != "my-parser" {
		t.Errorf("expected module name my-parser, got %s", pkgInfo.Config.Name)
	}

	// VerifyStructure alias check
	mainFile, err := management.VerifyStructure(dir)
	if err != nil {
		t.Fatalf("VerifyStructure failed: %v", err)
	}
	if filepath.Base(mainFile) != "main.go" {
		t.Errorf("expected main.go, got %s", mainFile)
	}
}

func TestVerifyStructure_MissingConfigZero(t *testing.T) {
	dir := createTestPkgDir(t, map[string]string{
		"readme.md": "# Docs",
		"main.go":   "package main\nfunc main() {}",
	})
	defer os.RemoveAll(dir)

	_, err := management.VerifyModulePackage(dir)
	if err == nil {
		t.Fatal("expected error for missing config.zero, got nil")
	}
}

func TestVerifyStructure_MissingReadme(t *testing.T) {
	dir := createTestPkgDir(t, map[string]string{
		"config.zero": "name: test\n",
		"main.go":     "package main\nfunc main() {}",
	})
	defer os.RemoveAll(dir)

	_, err := management.VerifyModulePackage(dir)
	if err == nil {
		t.Fatal("expected error for missing readme.md, got nil")
	}
}

func TestVerifyStructure_EmptyConfigZero(t *testing.T) {
	dir := createTestPkgDir(t, map[string]string{
		"config.zero": "   \n",
		"readme.md":   "# Docs",
		"main.go":     "package main\nfunc main() {}",
	})
	defer os.RemoveAll(dir)

	_, err := management.VerifyModulePackage(dir)
	if err == nil {
		t.Fatal("expected error for empty config.zero, got nil")
	}
}

func TestVerifyStructure_MissingPackageMain(t *testing.T) {
	dir := createTestPkgDir(t, map[string]string{
		"config.zero": "name: test\n",
		"readme.md":   "# Docs",
		"lib.go": `package mylib

func DoWork() {}
`,
	})
	defer os.RemoveAll(dir)

	_, err := management.VerifyModulePackage(dir)
	if err == nil {
		t.Fatal("expected error for missing package main, got nil")
	}
}

func TestVerifyStructure_MissingFuncMain(t *testing.T) {
	dir := createTestPkgDir(t, map[string]string{
		"config.zero": "name: test\n",
		"readme.md":   "# Docs",
		"main.go": `package main

var x = 10
`,
	})
	defer os.RemoveAll(dir)

	_, err := management.VerifyModulePackage(dir)
	if err == nil {
		t.Fatal("expected error for missing func main(), got nil")
	}
}

func TestVerifyStructure_NoGoFiles(t *testing.T) {
	dir := createTestPkgDir(t, map[string]string{
		"config.zero": "name: test\n",
		"readme.md":   "# Docs",
	})
	defer os.RemoveAll(dir)

	_, err := management.VerifyModulePackage(dir)
	if err == nil {
		t.Fatal("expected error for empty go directory, got nil")
	}
}

func TestAuditSecurity_CleanCode(t *testing.T) {
	dir := createTestPkgDir(t, map[string]string{
		"main.go": `package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

func main() {
	m := map[string]string{"key": strings.ToUpper("val")}
	b, _ := json.Marshal(m)
	fmt.Println(string(b))
}
`,
	})
	defer os.RemoveAll(dir)

	report, err := management.AuditSecurity(dir)
	if err != nil {
		t.Fatalf("unexpected audit error: %v", err)
	}
	if !report.Approved {
		t.Fatalf("expected audit to pass, got violations: %+v", report.Violations)
	}
	if len(report.Violations) != 0 {
		t.Errorf("expected 0 violations, got %d", len(report.Violations))
	}
}

func TestAuditSecurity_ForbiddenImports(t *testing.T) {
	tests := []struct {
		name       string
		sourceCode string
		rule       string
	}{
		{
			name: "os/exec import",
			sourceCode: `package main
import "os/exec"
func main() { exec.Command("ls") }`,
			rule: "FORBIDDEN_IMPORT",
		},
		{
			name: "syscall import",
			sourceCode: `package main
import "syscall"
func main() { _ = syscall.SIGKILL }`,
			rule: "FORBIDDEN_IMPORT",
		},
		{
			name: "unsafe import",
			sourceCode: `package main
import "unsafe"
func main() { _ = unsafe.Sizeof(1) }`,
			rule: "FORBIDDEN_IMPORT",
		},
		{
			name: "plugin import",
			sourceCode: `package main
import "plugin"
func main() { _, _ = plugin.Open("p.so") }`,
			rule: "FORBIDDEN_IMPORT",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := createTestPkgDir(t, map[string]string{
				"main.go": tt.sourceCode,
			})
			defer os.RemoveAll(dir)

			report, err := management.AuditSecurity(dir)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if report.Approved {
				t.Fatalf("expected security audit to fail for %s", tt.name)
			}
			found := false
			for _, v := range report.Violations {
				if v.Rule == tt.rule {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("expected violation rule %s, got %+v", tt.rule, report.Violations)
			}
		})
	}
}

func TestAuditSecurity_NetworkListenerBackdoor(t *testing.T) {
	dir := createTestPkgDir(t, map[string]string{
		"main.go": `package main

import "net"

func main() {
	l, _ := net.Listen("tcp", ":8080")
	_ = l
}
`,
	})
	defer os.RemoveAll(dir)

	report, err := management.AuditSecurity(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Approved {
		t.Fatal("expected audit to fail for net.Listen backdoor")
	}

	found := false
	for _, v := range report.Violations {
		if v.Rule == "NETWORK_LISTENER_BACKDOOR" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected NETWORK_LISTENER_BACKDOOR rule violation, got: %+v", report.Violations)
	}
}

func TestAuditSecurity_ReverseShellLiteral(t *testing.T) {
	dir := createTestPkgDir(t, map[string]string{
		"main.go": `package main

func main() {
	cmd := "/bin/bash"
	_ = cmd
}
`,
	})
	defer os.RemoveAll(dir)

	report, err := management.AuditSecurity(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Approved {
		t.Fatal("expected audit to fail for reverse shell literal")
	}

	found := false
	for _, v := range report.Violations {
		if v.Rule == "SHELL_LITERAL_DETECTED" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected SHELL_LITERAL_DETECTED rule violation, got: %+v", report.Violations)
	}
}
