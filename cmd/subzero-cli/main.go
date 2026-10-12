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

	"github.com/rachmanzz/subzero/pkg/management"
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
  module       Manage, audit, package, and publish tenant Wasm modules
    pack       Package a Go source directory into a safe .tar.gz bundle
    audit      Audit a Go directory or .tar.gz archive for security & backdoor vectors
    publish    Audit, bundle, and push a module to the Subzero Management API
    list       List registered Wasm modules on the management server
    download   Download a published .wasm artifact from the management server
  task         Submit and benchmark tasks against the worker/supervisor engine
    submit     Submit a single data normalization task to a worker

Run 'subzero-cli <command> <subcommand> -h' for more details on a command.`)
}

// ============================================================================
// MODULE COMMAND HANDLERS
// ============================================================================

func handleModuleCommand(args []string) error {
	if len(args) == 0 {
		fmt.Println(`Usage: subzero-cli module <pack | audit | publish | list | download> [options]`)
		return nil
	}

	sub := args[0]
	switch sub {
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
	default:
		return fmt.Errorf("unknown module subcommand %q. Run 'subzero-cli module' for help", sub)
	}
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
		fmt.Println("🔍 Running pre-packaging AST structure and security audit...")
		if _, err := management.VerifyStructure(absDir); err != nil {
			return fmt.Errorf("structure check failed: %w", err)
		}
		fmt.Println("  ✓ Package main and func main() verified")

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

	fmt.Printf("🔍 Auditing Go AST in: %s\n", auditDir)

	mainFile, err := management.VerifyStructure(auditDir)
	if err != nil {
		fmt.Printf("❌ Structure Verification Failed: %v\n", err)
	} else {
		fmt.Printf("✓ Structure Verified: valid entrypoint in %s\n", filepath.Base(mainFile))
	}

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
			if _, err := management.VerifyStructure(target); err != nil {
				return fmt.Errorf("local structure check failed: %w", err)
			}
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

// ============================================================================
// TASK COMMAND HANDLERS
// ============================================================================

func handleTaskCommand(args []string) error {
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
