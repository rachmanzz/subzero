package management_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	if pkgInfo.ReadmeContent != "# Parser Documentation\nNormalizes data." {
		t.Errorf("expected ReadmeContent to be populated, got: %q", pkgInfo.ReadmeContent)
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
		{
			name: "runtime/cgo import",
			sourceCode: `package main
import _ "runtime/cgo"
func main() {}`,
			rule: "FORBIDDEN_IMPORT",
		},
		{
			name: "cgo literal import C",
			sourceCode: `package main
/*
#include <stdio.h>
*/
import "C"
func main() {}`,
			rule: "CGO_FORBIDDEN",
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
	for _, call := range []string{`net.Listen("tcp", ":8080")`, `net.ListenPacket("udp", ":8080")`} {
		dir := createTestPkgDir(t, map[string]string{
			"main.go": fmt.Sprintf(`package main
import "net"
func main() {
	l, _ := %s
	_ = l
}
`, call),
		})
		defer os.RemoveAll(dir)

		report, err := management.AuditSecurity(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if report.Approved {
			t.Fatalf("expected audit to fail for %s backdoor", call)
		}

		found := false
		for _, v := range report.Violations {
			if v.Rule == "NETWORK_LISTENER_BACKDOOR" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected NETWORK_LISTENER_BACKDOOR rule violation for %s, got: %+v", call, report.Violations)
		}
	}
}

func TestAuditSecurity_ReverseShellLiteral(t *testing.T) {
	for _, shell := range []string{"/bin/bash", "/bin/sh", "cmd.exe", "powershell"} {
		dir := createTestPkgDir(t, map[string]string{
			"main.go": fmt.Sprintf(`package main
func main() {
	cmd := %q
	_ = cmd
}
`, shell),
		})
		defer os.RemoveAll(dir)

		report, err := management.AuditSecurity(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if report.Approved {
			t.Fatalf("expected audit to fail for shell literal %q", shell)
		}

		found := false
		for _, v := range report.Violations {
			if v.Rule == "SHELL_LITERAL_DETECTED" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected SHELL_LITERAL_DETECTED rule violation for %s, got: %+v", shell, report.Violations)
		}
	}
}

func TestVerifyStructure_PermissionsValid(t *testing.T) {
	dir := createTestPkgDir(t, map[string]string{
		"config.zero": `name: perm-module
permissions.host_calls: storage:get_object, logger:debug
permissions.filesystem.read_only: /tmp/subzero-tenants/in
permissions.filesystem.read_write: /tmp/subzero-workspace/out
permissions.s3.allowed_prefixes: s3://my-bucket/data/
permissions.files.allowed_extensions: .csv, .json
`,
		"readme.md": "# Docs",
		"main.go": `package main
func main() {}
`,
	})
	defer os.RemoveAll(dir)

	pkgInfo, err := management.VerifyModulePackage(dir)
	if err != nil {
		t.Fatalf("expected valid package with permissions, got error: %v", err)
	}
	if pkgInfo.Config.Permissions == nil {
		t.Fatal("expected permissions to be parsed, got nil")
	}
	if len(pkgInfo.Config.Permissions.HostCalls) != 2 {
		t.Errorf("expected 2 host calls, got %d", len(pkgInfo.Config.Permissions.HostCalls))
	}
	if len(pkgInfo.Config.Permissions.Filesystem.ReadOnly) != 1 {
		t.Errorf("expected 1 read-only path, got %d", len(pkgInfo.Config.Permissions.Filesystem.ReadOnly))
	}
}

func TestVerifyStructure_PermissionsSensitivePathReject(t *testing.T) {
	dir := createTestPkgDir(t, map[string]string{
		"config.zero": `name: bad-perm-module
permissions.filesystem.read_only: /etc/shadow
`,
		"readme.md": "# Docs",
		"main.go": `package main
func main() {}
`,
	})
	defer os.RemoveAll(dir)

	_, err := management.VerifyModulePackage(dir)
	if err == nil {
		t.Fatal("expected error for sensitive path /etc/shadow, got nil")
	}
}

func TestVerifyStructure_PermissionsPathTraversalReject(t *testing.T) {
	for _, badPath := range []string{"/tmp/../../etc/passwd", "/tmp/nested/../../data"} {
		dir := createTestPkgDir(t, map[string]string{
			"config.zero": fmt.Sprintf("name: bad-perm-module\npermissions.filesystem.read_only: %s\n", badPath),
			"readme.md":   "# Docs",
			"main.go": `package main
func main() {}
`,
		})
		_, err := management.VerifyModulePackage(dir)
		_ = os.RemoveAll(dir)
		if err == nil || !strings.Contains(err.Error(), "path traversal forbidden") {
			t.Fatalf("expected 'path traversal forbidden' error for path %q, got: %v", badPath, err)
		}
	}
}

func TestVerifyStructure_PermissionsValidPathWithDotsInSegment(t *testing.T) {
	validPath := "/tmp/backup..v2/input"
	dir := createTestPkgDir(t, map[string]string{
		"config.zero": fmt.Sprintf("name: dots-module\npermissions.filesystem.read_only: %s\n", validPath),
		"readme.md":   "# Docs",
		"main.go": `package main
func main() {}
`,
	})
	defer os.RemoveAll(dir)

	pkgInfo, err := management.VerifyModulePackage(dir)
	if err != nil {
		t.Fatalf("expected valid path %q with dots in segment to be accepted, got: %v", validPath, err)
	}
	if len(pkgInfo.Config.Permissions.Filesystem.ReadOnly) == 0 || pkgInfo.Config.Permissions.Filesystem.ReadOnly[0] != validPath {
		t.Fatalf("expected path %q in ReadOnly permissions, got: %+v", validPath, pkgInfo.Config.Permissions.Filesystem.ReadOnly)
	}
}

func TestAuditSecurity_NestedSubdirectoryForbiddenImport(t *testing.T) {
	dir := createTestPkgDir(t, map[string]string{
		"main.go": `package main
func main() {}
`,
		"sub/helper/util.go": `package helper
import "os/exec"
func Run() { _ = exec.Command("sh") }
`,
	})
	defer os.RemoveAll(dir)

	report, err := management.AuditSecurity(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Approved {
		t.Fatal("expected security audit to fail for nested forbidden import")
	}

	var found bool
	for _, v := range report.Violations {
		if v.Rule == "FORBIDDEN_IMPORT" && v.File == "util.go" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected FORBIDDEN_IMPORT violation in nested util.go, got: %+v", report.Violations)
	}
}

func TestVerifyStructure_PermissionEscalationForbiddenPaths(t *testing.T) {
	escalationCases := []struct {
		name    string
		path    string
		isWrite bool
	}{
		{"sensitive /etc read", "/etc/shadow", false},
		{"sensitive /root read", "/root/.ssh/id_rsa", false},
		{"sensitive /dev write", "/dev/sda1", true},
		{"sensitive /proc read", "/proc/sys/kernel", false},
		{"sensitive /sys write", "/sys/class", true},
		{"relative path traversal attempt", "relative/path/to/data", false},
	}

	for _, tc := range escalationCases {
		t.Run(tc.name, func(t *testing.T) {
			permField := "permissions.filesystem.read_only"
			if tc.isWrite {
				permField = "permissions.filesystem.read_write"
			}

			dir := createTestPkgDir(t, map[string]string{
				"config.zero": fmt.Sprintf("name: escalate-test\n%s: %s\n", permField, tc.path),
				"readme.md":   "# Documentation",
				"main.go": `package main
func main() {}
`,
			})
			defer os.RemoveAll(dir)

			_, err := management.VerifyModulePackage(dir)
			if err == nil {
				t.Fatalf("expected permission escalation to be rejected for path %q, got nil", tc.path)
			}
			t.Logf("✓ Path %q safely rejected: %v", tc.path, err)
		})
	}
}
