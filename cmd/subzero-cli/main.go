// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rachmanzz/subzero/internal/storage"
	"github.com/rachmanzz/subzero/internal/storage/local"
	"github.com/rachmanzz/subzero/internal/storage/s3"
	"github.com/rachmanzz/subzero/pkg/management"
	"github.com/rachmanzz/subzero/pkg/permission"
	"github.com/rachmanzz/subzero/pkg/producer"
	"github.com/rachmanzz/subzero/pkg/protocol"
	_ "github.com/rachmanzz/subzero/pkg/transport/http"
	_ "github.com/rachmanzz/subzero/pkg/transport/inproc"
	_ "github.com/rachmanzz/subzero/pkg/transport/ipc"
	_ "github.com/rachmanzz/subzero/pkg/transport/tcp"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	switch cmd {
	case "module":
		if err := handleModuleCommand(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	case "task":
		if err := handleTaskCommand(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	case "-h", "--help", "help":
		printUsage()
	default:
		// Legacy fallback support for flags directly on root (e.g. subzero-cli -action=ping)
		if strings.HasPrefix(cmd, "-") {
			if err := handleLegacyFlags(); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`Subzero CLI - Distributed Scale-to-Zero Runtime & Module Control

Usage:
  subzero-cli <command> [subcommand] [flags]

Available Commands:
  module         Manage, audit, package, and publish tenant Wasm modules
    init         Scaffold a new standardized module directory (config.zero, readme.md, main.go)
    pack         Package a Go source directory into a safe .tar.gz bundle
    audit        Audit a Go directory or .tar.gz archive for security & backdoor vectors
    publish      Audit, bundle, and push a module to the Subzero Management API
    list         List registered Wasm modules on the management server
    download     Download a published .wasm artifact from the management server
    inspect-policy Inspect module capabilities and test compliance against worker policy (PRD 3 & 5.5)
  task           Submit and benchmark tasks against the worker/supervisor engine
    submit       Submit a single data normalization task to a worker
    request-upload Allocate ephemeral pre-signed direct upload slot (PRD 5.3 & 7)

Run 'subzero-cli <command> <subcommand> -h' for more details on a command.`)
}

// ============================================================================
// MODULE COMMAND HANDLERS
// ============================================================================

func handleModuleCommand(args []string) error {
	if len(args) == 0 {
		fmt.Println(`Usage: subzero-cli module <init | pack | audit | publish | list | download | inspect-policy> [options]`)
		return nil
	}

	sub := args[0]
	switch sub {
	case "init":
		return handleModuleInit(args[1:])
	case "pack":
		return handleModulePack(args[1:])
	case "audit":
		return handleModuleAudit(args[1:])
	case "publish":
		return handleModulePublish(args[1:])
	case "list":
		return handleModuleList(args[1:])
	case "download":
		return handleModuleDownload(args[1:])
	case "inspect-policy":
		return handleModuleInspectPolicy(args[1:])
	default:
		return fmt.Errorf("unknown module subcommand %q. Run 'subzero-cli module' for help", sub)
	}
}

func handleModuleInit(args []string) error {
	fs := flag.NewFlagSet("module init", flag.ExitOnError)
	name := fs.String("name", "my-transformer", "Module identifier name")
	tenant := fs.String("tenant", "default", "Tenant identifier")
	target := fs.String("target", "wasip1", "Target Wasm compilation architecture")
	fs.Usage = func() {
		fmt.Println("Usage: subzero-cli module init <directory> [flags]")
		fs.PrintDefaults()
	}
	positionals, err := parseFlexible(fs, args)
	if err != nil {
		return err
	}
	if len(positionals) < 1 {
		fs.Usage()
		return fmt.Errorf("target directory is required")
	}

	targetDir := positionals[0]
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// 1. config.zero
	cfgZero := fmt.Sprintf(`# Subzero Module Specification
name: %s
version: 1.0.0
tenant: %s
target: %s
entrypoint: main.go
description: Data transformation module for Subzero runtime
memory_limit_pages: 512
timeout: 10s

# Capability permissions requested by this module (evaluated against Worker Policy)
permissions.host_calls: storage:get_object, logger:debug
permissions.filesystem.read_only: /tmp/subzero-tenants/in
permissions.filesystem.read_write: /tmp/subzero-workspace/scratch
permissions.s3.allowed_prefixes: s3://%s-vault/
permissions.files.allowed_extensions: .csv, .xlsx, .json
`, *name, *tenant, *target, *tenant)
	if err := os.WriteFile(filepath.Join(targetDir, "config.zero"), []byte(cfgZero), 0644); err != nil {
		return err
	}

	// 2. readme.md
	readme := fmt.Sprintf(`# %s Module

Documentation for %s Subzero WebAssembly module.

## Purpose
Transforms incoming tenant documents into standardized canonical format.

## Input & Output
- **Input**: CSV / Excel / Raw streams via Stdin or SourceURI.
- **Output**: Cleaned and validated data records via Stdout.
`, *name, *name)
	if err := os.WriteFile(filepath.Join(targetDir, "readme.md"), []byte(readme), 0644); err != nil {
		return err
	}

	// 3. main.go
	mainGo := `package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// Response defines standard structured output emitted to stdout for Subzero host
type Response struct {
	IsSuccess     bool   ` + "`json:\"is_success\"`" + `
	ProcessedRows int    ` + "`json:\"processed_rows\"`" + `
	Data          string ` + "`json:\"data,omitempty\"`" + `
}

func main() {
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to read stdin: %v\n", err)
		os.Exit(1)
	}

	// Execute custom schema transformation logic here
	resp := Response{
		IsSuccess:     true,
		ProcessedRows: 1,
		Data:          string(input),
	}

	_ = json.NewEncoder(os.Stdout).Encode(resp)
}
`
	if err := os.WriteFile(filepath.Join(targetDir, "main.go"), []byte(mainGo), 0644); err != nil {
		return err
	}

	// 4. go.mod
	goMod := fmt.Sprintf("module %s\n\ngo 1.22\n", *name)
	if err := os.WriteFile(filepath.Join(targetDir, "go.mod"), []byte(goMod), 0644); err != nil {
		return err
	}

	fmt.Printf("✅ Initialized Subzero module scaffold at: %s\n", targetDir)
	fmt.Println("   ├── config.zero  (Module runtime specification)")
	fmt.Println("   ├── readme.md    (Documentation & schema)")
	fmt.Println("   ├── main.go      (Go entrypoint: package main, func main)")
	fmt.Println("   └── go.mod       (Go module definition)")
	return nil
}

func handleModulePack(args []string) error {
	fs := flag.NewFlagSet("module pack", flag.ExitOnError)
	output := fs.String("o", "", "Output .tar.gz file path (default: <dir>.tar.gz)")
	skipAudit := fs.Bool("skip-audit", false, "Skip local AST structure & security auditing")
	fs.Usage = func() {
		fmt.Println("Usage: subzero-cli module pack <directory> [flags]")
		fs.PrintDefaults()
	}
	positionals, err := parseFlexible(fs, args)
	if err != nil {
		return err
	}

	if len(positionals) < 1 {
		fs.Usage()
		return fmt.Errorf("directory path is required")
	}

	srcDir := positionals[0]
	absDir, err := filepath.Abs(srcDir)
	if err != nil {
		return err
	}

	fmt.Printf("📦 Packaging module from: %s\n", absDir)

	if !*skipAudit {
		fmt.Println("🔍 Running pre-packaging structure and security audit...")
		pkgInfo, err := management.VerifyModulePackage(absDir)
		if err != nil {
			return fmt.Errorf("structure check failed: %w", err)
		}
		fmt.Printf("  ✓ config.zero verified (name: %s, target: %s)\n", pkgInfo.Config.Name, pkgInfo.Config.Target)
		fmt.Println("  ✓ readme.md documentation verified")
		fmt.Printf("  ✓ Entrypoint %s verified (package main, func main())\n", filepath.Base(pkgInfo.MainFile))

		report, err := management.AuditSecurity(absDir)
		if err != nil {
			return fmt.Errorf("security audit error: %w", err)
		}
		printAuditReport(report)
		if !report.Approved {
			return fmt.Errorf("audit rejected: security violations detected in source code")
		}
	}

	archiveBytes, err := management.PackTarGz(absDir)
	if err != nil {
		return fmt.Errorf("packaging failed: %w", err)
	}

	outPath := *output
	if outPath == "" {
		outPath = filepath.Base(absDir) + ".tar.gz"
	}

	if err := os.WriteFile(outPath, archiveBytes, 0644); err != nil {
		return fmt.Errorf("failed to write bundle: %w", err)
	}

	fmt.Printf("✅ Archive successfully created: %s (%.2f KB)\n", outPath, float64(len(archiveBytes))/1024.0)
	return nil
}

func handleModuleAudit(args []string) error {
	fs := flag.NewFlagSet("module audit", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Println("Usage: subzero-cli module audit <directory | archive.tar.gz>")
		fs.PrintDefaults()
	}
	positionals, err := parseFlexible(fs, args)
	if err != nil {
		return err
	}

	if len(positionals) < 1 {
		fs.Usage()
		return fmt.Errorf("target directory or archive path is required")
	}

	target := positionals[0]
	fi, err := os.Stat(target)
	if err != nil {
		return err
	}

	auditDir := target
	if !fi.IsDir() {
		// Target is an archive file
		archiveBytes, err := os.ReadFile(target)
		if err != nil {
			return err
		}

		tmpDir, err := os.MkdirTemp("", "subzero-cli-audit-*")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tmpDir)

		fmt.Printf("📂 Unpacking archive for security analysis: %s\n", target)
		if err := management.SafeUnpackTarGz(bytes.NewReader(archiveBytes), tmpDir, management.DefaultArchiveLimits()); err != nil {
			return fmt.Errorf("failed to unpack archive safely: %w", err)
		}
		auditDir = tmpDir
	}

	fmt.Printf("🔍 Auditing module package in: %s\n", auditDir)

	pkgInfo, err := management.VerifyModulePackage(auditDir)
	if err != nil {
		fmt.Printf("❌ Structure Verification Failed: %v\n", err)
		return err
	}
	fmt.Printf("✓ Structure Verified: module %q (entrypoint: %s)\n", pkgInfo.Config.Name, filepath.Base(pkgInfo.MainFile))

	report, err := management.AuditSecurity(auditDir)
	if err != nil {
		return fmt.Errorf("audit failed: %w", err)
	}

	printAuditReport(report)
	if !report.Approved || err != nil {
		return fmt.Errorf("module audit failed")
	}

	return nil
}

func handleModulePublish(args []string) error {
	fs := flag.NewFlagSet("module publish", flag.ExitOnError)
	serverURL := fs.String("server", "http://localhost:8080", "Subzero management server URL")
	tenantID := fs.String("tenant", "", "Tenant identifier (required)")
	moduleName := fs.String("name", "", "Module name (required)")
	targetOS := fs.String("target", "wasip1", "Target Wasm architecture (wasip1, js)")
	skipLocalAudit := fs.Bool("skip-audit", false, "Skip client-side local audit before upload")
	fs.Usage = func() {
		fmt.Println("Usage: subzero-cli module publish <directory | archive.tar.gz> [flags]")
		fs.PrintDefaults()
	}
	positionals, err := parseFlexible(fs, args)
	if err != nil {
		return err
	}

	if len(positionals) < 1 || *tenantID == "" || *moduleName == "" {
		fs.Usage()
		return fmt.Errorf("target file/dir, --tenant, and --name are required")
	}

	target := positionals[0]
	fi, err := os.Stat(target)
	if err != nil {
		return err
	}

	var uploadBytes []byte
	var uploadFilename string

	if fi.IsDir() {
		if !*skipLocalAudit {
			fmt.Println("🔍 Performing pre-flight local audit...")
			pkgInfo, err := management.VerifyModulePackage(target)
			if err != nil {
				return fmt.Errorf("local structure check failed: %w", err)
			}
			fmt.Printf("  ✓ config.zero verified (name: %s, target: %s)\n", pkgInfo.Config.Name, pkgInfo.Config.Target)
			fmt.Println("  ✓ readme.md documentation verified")
			fmt.Printf("  ✓ Entrypoint %s verified (package main, func main())\n", filepath.Base(pkgInfo.MainFile))
			report, err := management.AuditSecurity(target)
			if err != nil {
				return err
			}
			printAuditReport(report)
			if !report.Approved {
				return fmt.Errorf("local audit failed: aborting upload")
			}
		}

		fmt.Println("📦 Packing directory into .tar.gz bundle...")
		b, err := management.PackTarGz(target)
		if err != nil {
			return fmt.Errorf("packaging failed: %w", err)
		}
		uploadBytes = b
		uploadFilename = filepath.Base(target) + ".tar.gz"
	} else {
		b, err := os.ReadFile(target)
		if err != nil {
			return err
		}
		uploadBytes = b
		uploadFilename = filepath.Base(target)
	}

	fmt.Printf("🚀 Uploading to Subzero Management Server: %s/api/modules/compile\n", *serverURL)

	// Build multipart upload
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("tenant_id", *tenantID)
	_ = writer.WriteField("module_name", *moduleName)
	_ = writer.WriteField("target_os", *targetOS)

	part, err := writer.CreateFormFile("file", uploadFilename)
	if err != nil {
		return err
	}
	if _, err := part.Write(uploadBytes); err != nil {
		return err
	}
	_ = writer.Close()

	endpoint := strings.TrimRight(*serverURL, "/") + "/api/modules/compile"
	req, err := http.NewRequest(http.MethodPost, endpoint, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("network error during upload: %w", err)
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)

	var res map[string]any
	if err := json.Unmarshal(respBytes, &res); err != nil {
		return fmt.Errorf("server responded with status %d: %s", resp.StatusCode, string(respBytes))
	}

	if resp.StatusCode != http.StatusOK {
		fmt.Printf("\n⛔ Server Rejected Module (HTTP %d):\n", resp.StatusCode)
		fmt.Printf("  Stage   : %v\n", res["stage"])
		fmt.Printf("  Error   : %v\n", res["error"])
		if violations, ok := res["violations"].([]any); ok {
			fmt.Printf("  Violations Detected (%d):\n", len(violations))
			for _, v := range violations {
				vm, _ := v.(map[string]any)
				fmt.Printf("    🚨 [%v] %v in %v:%v -> %v\n", vm["severity"], vm["rule"], vm["file"], vm["line"], vm["message"])
			}
		}
		return fmt.Errorf("publication rejected by server")
	}

	fmt.Println("\n🎉 Module Successfully Verified, Compiled & Published!")
	fmt.Printf("  Module ID    : %v\n", res["module_id"])
	fmt.Printf("  Status       : 🛡️ PUBLISHED & VERIFIED SAFE\n")
	fmt.Printf("  Wasm Size    : %.2f MB\n", res["size"].(float64)/(1024*1024))
	fmt.Printf("  Build Time   : %v\n", res["duration"])
	if audit, ok := res["audit"].(map[string]any); ok {
		fmt.Printf("  Security     : %v file(s) scanned, risk score %v\n", audit["scanned_files"], audit["risk_score"])
	}
	return nil
}

func handleModuleList(args []string) error {
	fs := flag.NewFlagSet("module list", flag.ExitOnError)
	serverURL := fs.String("server", "http://localhost:8080", "Subzero management server URL")
	tenantID := fs.String("tenant", "", "Optional filter by Tenant ID")
	if _, err := parseFlexible(fs, args); err != nil {
		return err
	}

	endpoint := strings.TrimRight(*serverURL, "/") + "/api/modules"
	if *tenantID != "" {
		endpoint += "?tenant_id=" + *tenantID
	}

	resp, err := http.Get(endpoint)
	if err != nil {
		return fmt.Errorf("failed to query management server: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server error %d: %s", resp.StatusCode, string(body))
	}

	var modules []*management.WasmModule
	if err := json.NewDecoder(resp.Body).Decode(&modules); err != nil {
		return err
	}

	if len(modules) == 0 {
		fmt.Println("No Wasm modules registered on server.")
		return nil
	}

	fmt.Printf("%-35s %-15s %-20s %-8s %-12s %-10s %s\n", "MODULE ID", "TENANT", "NAME", "TARGET", "STATUS", "SIZE", "CREATED")
	fmt.Println(strings.Repeat("-", 115))
	for _, m := range modules {
		status := m.Status
		if status == "" {
			status = "PUBLISHED"
		}
		sizeMB := fmt.Sprintf("%.2f MB", float64(m.Size)/(1024*1024))
		fmt.Printf("%-35s %-15s %-20s %-8s %-12s %-10s %s\n",
			m.ID, m.TenantID, m.Name, m.Target, status, sizeMB, m.CreatedAt.Format("2006-01-02 15:04"))
	}
	return nil
}

func handleModuleDownload(args []string) error {
	fs := flag.NewFlagSet("module download", flag.ExitOnError)
	serverURL := fs.String("server", "http://localhost:8080", "Subzero management server URL")
	output := fs.String("o", "", "Output filename (default: <module_id>.wasm)")
	positionals, err := parseFlexible(fs, args)
	if err != nil {
		return err
	}

	if len(positionals) < 1 {
		return fmt.Errorf("module ID is required")
	}

	moduleID := positionals[0]
	endpoint := fmt.Sprintf("%s/api/modules/%s/download", strings.TrimRight(*serverURL, "/"), moduleID)

	resp, err := http.Get(endpoint)
	if err != nil {
		return fmt.Errorf("network error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("download failed (HTTP %d): %s", resp.StatusCode, string(body))
	}

	outFile := *output
	if outFile == "" {
		outFile = moduleID + ".wasm"
	}

	f, err := os.Create(outFile)
	if err != nil {
		return err
	}
	defer f.Close()

	written, err := io.Copy(f, resp.Body)
	if err != nil {
		return err
	}

	fmt.Printf("✓ Downloaded %s (%.2f MB)\n", outFile, float64(written)/(1024*1024))
	return nil
}

func printAuditReport(report *management.AuditReport) {
	fmt.Printf("  Files Scanned  : %d\n", report.ScannedFiles)
	fmt.Printf("  Risk Score     : %d\n", report.RiskScore)
	if report.Approved {
		fmt.Println("  Status         : 🛡️ APPROVED (Pristine Go AST, zero backdoors)")
	} else {
		fmt.Printf("  Status         : ⛔ REJECTED (%d violations detected)\n", len(report.Violations))
		for _, v := range report.Violations {
			fmt.Printf("    🚨 [%s] %s at %s:%d: %s\n", v.Severity, v.Rule, v.File, v.Line, v.Message)
		}
	}
}

func handleModuleInspectPolicy(args []string) error {
	fs := flag.NewFlagSet("module inspect-policy", flag.ExitOnError)
	moduleID := fs.String("id", "", "Module ID registered on the management server")
	modulePath := fs.String("module", "", "Local module directory or .tar.gz bundle")
	policyPath := fs.String("policy", "", "Path to worker policy JSON file")
	serverURL := fs.String("server", "http://localhost:8080", "Subzero Management Server URL")

	positionals, err := parseFlexible(fs, args)
	if err != nil {
		return err
	}
	if *modulePath == "" && len(positionals) > 0 {
		*modulePath = positionals[0]
	}

	if *moduleID == "" && *modulePath == "" {
		return fmt.Errorf("either -id (server module) or -module (local module directory/bundle) must be provided")
	}

	var workerPolicy *permission.WorkerPolicy
	if *policyPath != "" {
		policyData, err := os.ReadFile(*policyPath)
		if err != nil {
			return fmt.Errorf("failed to read policy file: %w", err)
		}
		var wp permission.WorkerPolicy
		if err := json.Unmarshal(policyData, &wp); err != nil {
			return fmt.Errorf("failed to parse policy json: %w", err)
		}
		workerPolicy = &wp
	}

	var resp management.InspectPolicyResponse

	if *moduleID != "" && *modulePath == "" {
		// Inspect published module on server
		endpoint := fmt.Sprintf("%s/api/modules/%s/inspect-policy", strings.TrimRight(*serverURL, "/"), *moduleID)
		reqPayload := management.InspectPolicyRequest{
			WorkerPolicy: workerPolicy,
		}
		payloadBytes, _ := json.Marshal(reqPayload)

		req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(payloadBytes))
		if err != nil {
			return fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		client := &http.Client{Timeout: 10 * time.Second}
		httpResp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("failed to query management server: %w", err)
		}
		defer httpResp.Body.Close()

		if httpResp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(httpResp.Body)
			return fmt.Errorf("inspection failed (HTTP %d): %s", httpResp.StatusCode, string(body))
		}

		if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
			return fmt.Errorf("failed to decode response: %w", err)
		}
	} else {
		// Inspect local module directory or bundle
		var modReq *permission.ModuleRequest
		modID := *moduleID
		if modID == "" {
			modID = filepath.Base(*modulePath)
		}

		fi, err := os.Stat(*modulePath)
		if err != nil {
			return fmt.Errorf("cannot access module path %s: %w", *modulePath, err)
		}

		var cfgZero *management.ConfigZero
		if fi.IsDir() {
			pkgInfo, err := management.VerifyModulePackage(*modulePath)
			if err != nil {
				return fmt.Errorf("module package verification failed: %w", err)
			}
			cfgZero = pkgInfo.Config
		} else if strings.HasSuffix(*modulePath, ".tar.gz") || strings.HasSuffix(*modulePath, ".tgz") {
			tempDir, err := os.MkdirTemp("", "subzero-inspect-unpack-*")
			if err != nil {
				return err
			}
			defer os.RemoveAll(tempDir)

			f, err := os.Open(*modulePath)
			if err != nil {
				return err
			}
			defer f.Close()

			if err := management.SafeUnpackTarGz(f, tempDir, management.DefaultArchiveLimits()); err != nil {
				return fmt.Errorf("failed to unpack archive: %w", err)
			}
			pkgInfo, err := management.VerifyModulePackage(tempDir)
			if err != nil {
				return fmt.Errorf("archive module verification failed: %w", err)
			}
			cfgZero = pkgInfo.Config
		} else if strings.HasSuffix(*modulePath, "config.zero") {
			data, err := os.ReadFile(*modulePath)
			if err != nil {
				return err
			}
			c, err := management.ParseConfigZero(data)
			if err != nil {
				return err
			}
			cfgZero = c
		} else {
			return fmt.Errorf("unsupported module format: must be directory, .tar.gz bundle, or config.zero file")
		}

		if cfgZero != nil {
			modReq = cfgZero.Permissions
			if modID == "" || modID == filepath.Base(*modulePath) {
				modID = cfgZero.Name
			}
		}

		// Try sending to management server endpoint /api/inspect-policy
		endpoint := fmt.Sprintf("%s/api/inspect-policy", strings.TrimRight(*serverURL, "/"))
		reqPayload := management.InspectPolicyRequest{
			ModuleRequest: modReq,
			WorkerPolicy:  workerPolicy,
		}
		payloadBytes, _ := json.Marshal(reqPayload)

		req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(payloadBytes))
		var serverSuccess bool
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			client := &http.Client{Timeout: 5 * time.Second}
			httpResp, err := client.Do(req)
			if err == nil {
				defer httpResp.Body.Close()
				if httpResp.StatusCode == http.StatusOK {
					if err := json.NewDecoder(httpResp.Body).Decode(&resp); err == nil {
						serverSuccess = true
					}
				}
			}
		}

		// Fallback to local evaluation if server was unreachable or not running
		if !serverSuccess {
			wp := workerPolicy
			if wp == nil {
				defPolicy := permission.DefaultWorkerPolicy()
				wp = &defPolicy
			}
			eff, evalErr := permission.Evaluate(wp, modReq)
			resp = management.InspectPolicyResponse{
				ModuleID:             modID,
				Allowed:              evalErr == nil,
				RequestedPermissions: modReq,
				EffectivePermissions: eff,
			}
			if evalErr != nil {
				resp.Error = evalErr.Error()
			}
		}
	}

	printInspectReport(&resp)
	if !resp.Allowed {
		return fmt.Errorf("module capability negotiation failed: %s", resp.Error)
	}
	return nil
}

func printInspectReport(resp *management.InspectPolicyResponse) {
	fmt.Println("📋 Module Capability Policy Inspection (PRD 3 & 5.5)")
	if resp.ModuleID != "" {
		fmt.Printf("  Module ID   : %s\n", resp.ModuleID)
	}
	if resp.TenantID != "" {
		fmt.Printf("  Tenant ID   : %s\n", resp.TenantID)
	}
	if resp.Allowed {
		fmt.Println("  Decision    : 🛡️ APPROVED (Module complies with Worker Policy)")
	} else {
		fmt.Println("  Decision    : ⛔ REJECTED")
		if resp.Error != "" {
			fmt.Printf("  Reason      : %s\n", resp.Error)
		}
	}

	if resp.RequestedPermissions != nil {
		fmt.Println("  Requested Capabilities:")
		if len(resp.RequestedPermissions.HostCalls) > 0 {
			fmt.Printf("    Host Calls          : %v\n", resp.RequestedPermissions.HostCalls)
		}
		if len(resp.RequestedPermissions.Filesystem.ReadOnly) > 0 {
			fmt.Printf("    Read-Only FS        : %v\n", resp.RequestedPermissions.Filesystem.ReadOnly)
		}
		if len(resp.RequestedPermissions.Filesystem.ReadWrite) > 0 {
			fmt.Printf("    Read-Write FS       : %v\n", resp.RequestedPermissions.Filesystem.ReadWrite)
		}
		if len(resp.RequestedPermissions.S3.AllowedPrefixes) > 0 {
			fmt.Printf("    S3 Allowed Prefixes : %v\n", resp.RequestedPermissions.S3.AllowedPrefixes)
		}
		if len(resp.RequestedPermissions.Files.AllowedExtensions) > 0 {
			fmt.Printf("    Allowed Extensions  : %v\n", resp.RequestedPermissions.Files.AllowedExtensions)
		}
		if resp.RequestedPermissions.Files.MaxFileSizeMB > 0 {
			fmt.Printf("    Max File Size (MB)  : %d MB\n", resp.RequestedPermissions.Files.MaxFileSizeMB)
		}
	}

	if resp.EffectivePermissions != nil && resp.Allowed {
		fmt.Println("  Effective Permissions Granted:")
		if len(resp.EffectivePermissions.HostCalls) > 0 {
			var calls []string
			for k, v := range resp.EffectivePermissions.HostCalls {
				if v {
					calls = append(calls, k)
				}
			}
			fmt.Printf("    Allowed Host Calls  : %v\n", calls)
		}
		if len(resp.EffectivePermissions.Filesystem.ReadOnly) > 0 {
			fmt.Printf("    Allowed Read Paths  : %v\n", resp.EffectivePermissions.Filesystem.ReadOnly)
		}
		if len(resp.EffectivePermissions.Filesystem.ReadWrite) > 0 {
			fmt.Printf("    Allowed Write Paths : %v\n", resp.EffectivePermissions.Filesystem.ReadWrite)
		}
		if len(resp.EffectivePermissions.S3.AllowedPrefixes) > 0 {
			fmt.Printf("    Allowed S3 Prefixes : %v\n", resp.EffectivePermissions.S3.AllowedPrefixes)
		}
		if len(resp.EffectivePermissions.Files.AllowedExtensions) > 0 {
			fmt.Printf("    Allowed Extensions  : %v\n", resp.EffectivePermissions.Files.AllowedExtensions)
		}
		if resp.EffectivePermissions.Files.MaxFileSizeBytes > 0 {
			fmt.Printf("    Max File Size       : %.2f MB\n", float64(resp.EffectivePermissions.Files.MaxFileSizeBytes)/(1024*1024))
		}
	}
}

// ============================================================================
// TASK COMMAND HANDLERS
// ============================================================================

func handleTaskCommand(args []string) error {
	if len(args) == 0 {
		return handleTaskSubmit(args)
	}

	sub := args[0]
	switch sub {
	case "submit":
		return handleTaskSubmit(args[1:])
	case "request-upload":
		return handleTaskRequestUpload(args[1:])
	case "-h", "--help", "help":
		fmt.Println(`Usage: subzero-cli task <submit | request-upload> [flags]

Available Subcommands:
  submit          Submit a normalization task to a worker
  request-upload  Allocate an ephemeral pre-signed direct upload slot (PRD 5.3 & 7)`)
		return nil
	default:
		// Fallback for direct flags (e.g. subzero-cli task -target=...)
		if strings.HasPrefix(sub, "-") {
			return handleTaskSubmit(args)
		}
		return fmt.Errorf("unknown task subcommand %q. Run 'subzero-cli task -h' for help", sub)
	}
}

func handleTaskSubmit(args []string) error {
	fs := flag.NewFlagSet("task submit", flag.ExitOnError)
	scheme := fs.String("scheme", "unix", "Transport scheme (unix, tcp, http)")
	endpoint := fs.String("endpoint", "/tmp/subzero-worker.sock", "Worker socket or address")
	secretKey := fs.String("secret", "subzero-secret-key-32b-default!", "HMAC secret key")
	tenant := fs.String("tenant", "demo-tenant", "Tenant ID")
	target := fs.String("target", "ping-test", "Action target / module ID")
	sourceURI := fs.String("source", "file:///tmp/sample.csv", "Source data URI")
	timeout := fs.Duration("timeout", 5*time.Second, "Request timeout")
	_ = fs.Parse(args)

	client, err := producer.New(producer.Config{
		Scheme:    *scheme,
		Endpoint:  *endpoint,
		SecretKey: []byte(*secretKey),
		Timeout:   *timeout,
	})
	if err != nil {
		return fmt.Errorf("failed to initialize client: %w", err)
	}

	task := &protocol.TaskMessage{
		TaskID:       fmt.Sprintf("cli-%d", time.Now().UnixNano()),
		TenantID:     *tenant,
		ActionTarget: *target,
		SourceURI:    *sourceURI,
	}

	start := time.Now()
	res, err := client.SubmitTask(context.Background(), task)
	latency := time.Since(start)

	if err != nil {
		return fmt.Errorf("task submission failed: %w", err)
	}

	fmt.Println("✓ Task Completed Successfully!")
	fmt.Printf("  Task ID        : %s\n", res.TaskID)
	fmt.Printf("  Success        : %t\n", res.IsSuccess)
	fmt.Printf("  Processed Rows : %d\n", res.ProcessedRows)
	fmt.Printf("  Round-Trip     : %v\n", latency)
	fmt.Printf("  Execution Time : %s\n", res.ExecutionTime)
	return nil
}

func handleTaskRequestUpload(args []string) error {
	fs := flag.NewFlagSet("task request-upload", flag.ExitOnError)
	storageRef := fs.String("ref", "", "Storage reference URI (e.g. s3://tenant-vault/input.csv, file:///tmp/input.csv)")
	ttl := fs.Duration("ttl", 15*time.Minute, "Expiration TTL (max 15 minutes per PRD 7)")
	scheme := fs.String("scheme", "", "Storage scheme (s3, minio, file). Inferred from -ref if omitted")
	bucket := fs.String("bucket", "", "S3 bucket name (inferred from s3://bucket/key if omitted)")
	endpoint := fs.String("endpoint", "", "Custom S3 / MinIO endpoint URL (e.g. http://localhost:9000)")
	region := fs.String("region", "us-east-1", "S3 region")
	accessKey := fs.String("access-key", "", "S3 access key")
	secretKey := fs.String("secret-key", "", "S3 secret key")
	pathStyle := fs.Bool("path-style", false, "Use path style addressing (recommended for MinIO/LocalStack)")

	positionals, err := parseFlexible(fs, args)
	if err != nil {
		return err
	}
	if *storageRef == "" && len(positionals) > 0 {
		*storageRef = positionals[0]
	}
	if *storageRef == "" {
		return fmt.Errorf("storage reference (-ref) is required")
	}

	validTTL, err := storage.ValidateEphemeralTTL(*ttl)
	if err != nil {
		return fmt.Errorf("invalid ttl: %w", err)
	}

	sc := *scheme
	if sc == "" {
		if strings.HasPrefix(*storageRef, "s3://") || strings.HasPrefix(*storageRef, "minio://") {
			sc = "s3"
		} else {
			sc = "file"
		}
	}

	var slot *protocol.UploadSlotResponse
	if sc == "s3" || sc == "minio" {
		b := *bucket
		if b == "" {
			trimmed := strings.TrimPrefix(*storageRef, "s3://")
			trimmed = strings.TrimPrefix(trimmed, "minio://")
			parts := strings.SplitN(trimmed, "/", 2)
			if len(parts) > 0 && parts[0] != "" {
				b = parts[0]
			}
		}
		if b == "" {
			return fmt.Errorf("s3 bucket name required or must be present in reference (e.g. s3://bucket/key)")
		}

		usePathStyle := *pathStyle
		if *endpoint != "" && !usePathStyle {
			usePathStyle = true
		}

		d, err := s3.New(s3.Config{
			Bucket:       b,
			Region:       *region,
			Endpoint:     *endpoint,
			AccessKey:    *accessKey,
			SecretKey:    *secretKey,
			UsePathStyle: usePathStyle,
		})
		if err != nil {
			return fmt.Errorf("failed to initialize s3 driver: %w", err)
		}

		slot, err = d.GenerateUploadSlot(context.Background(), *storageRef, validTTL)
		if err != nil {
			return fmt.Errorf("failed to generate upload slot: %w", err)
		}
	} else {
		// Local file driver
		baseDir := os.TempDir()
		d := local.New(baseDir)
		slot, err = d.GenerateUploadSlot(context.Background(), *storageRef, validTTL)
		if err != nil {
			return fmt.Errorf("failed to generate upload slot: %w", err)
		}
	}

	fmt.Println("✓ Pre-signed Upload Slot Allocated! (PRD 5.3 & 7)")
	fmt.Printf("  Storage Reference : %s\n", slot.StorageReference)
	fmt.Printf("  HTTP Method       : %s\n", slot.HTTPMethod)
	fmt.Printf("  Target URL        : %s\n", slot.TargetURL)
	fmt.Printf("  Expires At        : %s (TTL: %v)\n", slot.ExpiredAt.Format(time.RFC3339), validTTL)
	return nil
}

func handleLegacyFlags() error {
	scheme := flag.String("scheme", "unix", "Transport scheme (unix, tcp, http)")
	endpoint := flag.String("endpoint", "/tmp/subzero-worker.sock", "Worker socket or host address")
	secretKey := flag.String("secret", "subzero-secret-key-32b-default!", "HMAC secret key")
	tenant := flag.String("tenant", "demo-tenant", "Tenant ID")
	action := flag.String("action", "benchmark", "Action (benchmark | ping)")
	flag.Parse()

	client, err := producer.New(producer.Config{
		Scheme:    *scheme,
		Endpoint:  *endpoint,
		SecretKey: []byte(*secretKey),
		Timeout:   5 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("failed to initialize client: %w", err)
	}

	task := &protocol.TaskMessage{
		TaskID:       fmt.Sprintf("cli-%d", time.Now().UnixNano()),
		TenantID:     *tenant,
		ActionTarget: *action,
		SourceURI:    "file:///tmp/sample.csv",
	}

	start := time.Now()
	res, err := client.SubmitTask(context.Background(), task)
	latency := time.Since(start)

	if err != nil {
		return fmt.Errorf("task execution failed: %w", err)
	}

	fmt.Printf("✓ Task Completed Successfully!\n")
	fmt.Printf("  Task ID        : %s\n", res.TaskID)
	fmt.Printf("  Success        : %t\n", res.IsSuccess)
	fmt.Printf("  Round-Trip     : %v\n", latency)
	fmt.Printf("  Execution Time : %s\n", res.ExecutionTime)
	return nil
}

func parseFlexible(fs *flag.FlagSet, args []string) ([]string, error) {
	var flags []string
	var positionals []string

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
			flags = append(flags, arg)
			clean := strings.TrimLeft(arg, "-")
			if !strings.Contains(clean, "=") {
				f := fs.Lookup(clean)
				if f != nil {
					if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); !ok || !bf.IsBoolFlag() {
						if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
							i++
							flags = append(flags, args[i])
						}
					}
				}
			}
		} else {
			positionals = append(positionals, arg)
		}
	}
	if err := fs.Parse(flags); err != nil {
		return nil, err
	}
	return positionals, nil
}
