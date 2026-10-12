package management

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rachmanzz/subzero/pkg/permission"
)

var (
	ErrMissingConfigZero     = errors.New("structure: package must contain config.zero specification file")
	ErrMissingReadme         = errors.New("structure: package must contain readme.md documentation file")
	ErrEmptyConfigZero       = errors.New("structure: config.zero cannot be empty")
	ErrInvalidConfigZero     = errors.New("structure: config.zero is invalid or malformed")
	ErrInvalidMainEntrypoint = errors.New("structure: entrypoint does not contain package main or func main()")
)

// SecurityViolation describes a detected unsafe pattern or potential backdoor.
type SecurityViolation struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Severity string `json:"severity"` // "CRITICAL", "HIGH", "MEDIUM"
	Rule     string `json:"rule"`
	Message  string `json:"message"`
}

// AuditReport provides the outcome of the security validation and backdoor audit.
type AuditReport struct {
	Approved     bool                `json:"approved"`
	RiskScore    int                 `json:"risk_score"` // 0 is pristine, >0 indicates risks
	ScannedFiles int                 `json:"scanned_files"`
	Violations   []SecurityViolation `json:"violations"`
	ChecksPassed []string            `json:"checks_passed"`
	AuditedAt    time.Time           `json:"audited_at"`
}

var forbiddenImports = map[string]string{
	"os/exec":      "Command execution & shell spawning forbidden (backdoor risk)",
	"syscall":      "Low-level kernel traps & system calls forbidden",
	"unsafe":       "Direct memory tampering via unsafe pointers forbidden",
	"plugin":       "Dynamic shared library loading forbidden",
	"runtime/cgo":  "CGo native code invocation forbidden",
}

// ConfigZero defines the module metadata and runtime specification declared in config.zero.
type ConfigZero struct {
	Name             string                    `json:"name" yaml:"name"`
	Version          string                    `json:"version" yaml:"version"`
	Tenant           string                    `json:"tenant" yaml:"tenant"`
	Target           string                    `json:"target" yaml:"target"`                 // Default: "wasip1"
	Entrypoint       string                    `json:"entrypoint" yaml:"entrypoint"`         // Default: "main.go"
	Description      string                    `json:"description" yaml:"description"`
	MemoryLimitPages uint32                    `json:"memory_limit_pages" yaml:"memory_limit_pages"`
	Timeout          string                    `json:"timeout" yaml:"timeout"`
	Permissions      *permission.ModuleRequest `json:"permissions,omitempty" yaml:"permissions"`
}

// ModulePackageInfo contains the verified components of an uploaded module package.
type ModulePackageInfo struct {
	RootPath      string      `json:"root_path"`
	Config        *ConfigZero `json:"config"`
	MainFile      string      `json:"main_file"`
	ReadmePath    string      `json:"readme_path"`
	ReadmeContent string      `json:"readme_content,omitempty"`
}

// ParseConfigZero parses config.zero from JSON or Key-Value/YAML format.
func ParseConfigZero(data []byte) (*ConfigZero, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, ErrEmptyConfigZero
	}

	var cfg ConfigZero
	// Try JSON first if it starts with '{'
	if bytes.HasPrefix(trimmed, []byte("{")) {
		if err := json.Unmarshal(trimmed, &cfg); err != nil {
			return nil, fmt.Errorf("invalid json syntax: %w", err)
		}
	} else {
		// Key-Value / YAML-like scanner (key: value or key = value)
		scanner := bufio.NewScanner(bytes.NewReader(trimmed))
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
				continue
			}
			idx := strings.IndexAny(line, ":=")
			if idx == -1 {
				continue
			}
			k := strings.ToLower(strings.TrimSpace(line[:idx]))
			v := strings.TrimSpace(line[idx+1:])
			v = strings.Trim(v, `"'`)

			switch k {
			case "name":
				cfg.Name = v
			case "version":
				cfg.Version = v
			case "tenant", "tenant_id":
				cfg.Tenant = v
			case "target", "target_os":
				cfg.Target = v
			case "entrypoint", "main", "main_file":
				cfg.Entrypoint = v
			case "description":
				cfg.Description = v
			case "memory_limit_pages":
				var pages uint32
				_, _ = fmt.Sscanf(v, "%d", &pages)
				cfg.MemoryLimitPages = pages
			case "timeout":
				cfg.Timeout = v
			case "permissions.host_calls", "permission.host_calls":
				if cfg.Permissions == nil {
					cfg.Permissions = &permission.ModuleRequest{}
				}
				for _, item := range strings.Split(v, ",") {
					if c := strings.TrimSpace(item); c != "" {
						cfg.Permissions.HostCalls = append(cfg.Permissions.HostCalls, c)
					}
				}
			case "permissions.filesystem.read_only", "permissions.read_only":
				if cfg.Permissions == nil {
					cfg.Permissions = &permission.ModuleRequest{}
				}
				for _, item := range strings.Split(v, ",") {
					if c := strings.TrimSpace(item); c != "" {
						cfg.Permissions.Filesystem.ReadOnly = append(cfg.Permissions.Filesystem.ReadOnly, c)
					}
				}
			case "permissions.filesystem.read_write", "permissions.read_write":
				if cfg.Permissions == nil {
					cfg.Permissions = &permission.ModuleRequest{}
				}
				for _, item := range strings.Split(v, ",") {
					if c := strings.TrimSpace(item); c != "" {
						cfg.Permissions.Filesystem.ReadWrite = append(cfg.Permissions.Filesystem.ReadWrite, c)
					}
				}
			case "permissions.s3.allowed_prefixes", "permissions.s3_prefixes":
				if cfg.Permissions == nil {
					cfg.Permissions = &permission.ModuleRequest{}
				}
				for _, item := range strings.Split(v, ",") {
					if c := strings.TrimSpace(item); c != "" {
						cfg.Permissions.S3.AllowedPrefixes = append(cfg.Permissions.S3.AllowedPrefixes, c)
					}
				}
			case "permissions.files.allowed_extensions", "permissions.allowed_extensions":
				if cfg.Permissions == nil {
					cfg.Permissions = &permission.ModuleRequest{}
				}
				for _, item := range strings.Split(v, ",") {
					if c := strings.TrimSpace(item); c != "" {
						cfg.Permissions.Files.AllowedExtensions = append(cfg.Permissions.Files.AllowedExtensions, c)
					}
				}
			}
		}
	}

	if cfg.Entrypoint == "" {
		cfg.Entrypoint = "main.go"
	}
	if cfg.Target == "" {
		cfg.Target = "wasip1"
	}
	if cfg.Name == "" {
		return nil, errors.New("config.zero must specify module name")
	}

	return &cfg, nil
}

// VerifyModulePackage inspects the unpacked module directory, enforcing:
// 1. config.zero specification file exists and is valid
// 2. readme.md documentation file exists and is non-empty
// 3. Main entrypoint Go file exists, has 'package main' and 'func main()'
func VerifyModulePackage(rootPath string) (*ModulePackageInfo, error) {
	workDir := rootPath

	// Detect if archive was packed with a single root directory wrapping the files
	entries, err := os.ReadDir(rootPath)
	if err == nil && len(entries) == 1 && entries[0].IsDir() {
		candidate := filepath.Join(rootPath, entries[0].Name())
		if _, err := os.Stat(filepath.Join(candidate, "config.zero")); err == nil {
			workDir = candidate
		}
	}

	// 1. Verify config.zero
	cfgPath := filepath.Join(workDir, "config.zero")
	cfgData, err := os.ReadFile(cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrMissingConfigZero
		}
		return nil, fmt.Errorf("structure: read config.zero: %w", err)
	}

	cfg, err := ParseConfigZero(cfgData)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidConfigZero, err)
	}

	// 2. Verify readme.md (case-insensitive: readme.md, README.md, Readme.md)
	var readmePath string
	for _, name := range []string{"readme.md", "README.md", "Readme.md"} {
		p := filepath.Join(workDir, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			readmePath = p
			break
		}
	}
	if readmePath == "" {
		return nil, ErrMissingReadme
	}

	readmeData, err := os.ReadFile(readmePath)
	if err != nil {
		return nil, fmt.Errorf("structure: read readme: %w", err)
	}
	if len(bytes.TrimSpace(readmeData)) == 0 {
		return nil, fmt.Errorf("%w: file cannot be empty", ErrMissingReadme)
	}

	// 3. Verify main entrypoint file
	entrypointRel := cfg.Entrypoint
	mainPath := filepath.Join(workDir, entrypointRel)
	if _, err := os.Stat(mainPath); os.IsNotExist(err) {
		// Fallback check: look if any .go file in workDir contains func main
		var foundMain string
		_ = filepath.Walk(workDir, func(p string, fi os.FileInfo, err error) error {
			if err != nil || fi.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			fset := token.NewFileSet()
			if node, err := parser.ParseFile(fset, p, nil, parser.PackageClauseOnly); err == nil && node.Name.Name == "main" {
				foundMain = p
			}
			return nil
		})
		if foundMain != "" {
			mainPath = foundMain
		} else {
			return nil, fmt.Errorf("structure: entrypoint %q specified in config.zero does not exist", entrypointRel)
		}
	}

	// Verify package main and func main() via AST
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, mainPath, nil, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("structure: syntax error in main file %s: %w", filepath.Base(mainPath), err)
	}

	if node.Name.Name != "main" {
		return nil, fmt.Errorf("%w: %s has package %q (must be 'main')", ErrInvalidMainEntrypoint, filepath.Base(mainPath), node.Name.Name)
	}

	hasMainFunc := false
	for _, decl := range node.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "main" {
			hasMainFunc = true
			break
		}
	}
	if !hasMainFunc {
		return nil, fmt.Errorf("%w: %s is missing func main()", ErrInvalidMainEntrypoint, filepath.Base(mainPath))
	}

	// 4. Validate requested permissions safety if declared in config.zero
	if cfg.Permissions != nil {
		if err := validateRequestedPermissions(cfg.Permissions); err != nil {
			return nil, fmt.Errorf("structure: invalid permission request: %w", err)
		}
	}

	return &ModulePackageInfo{
		RootPath:      workDir,
		Config:        cfg,
		MainFile:      mainPath,
		ReadmePath:    readmePath,
		ReadmeContent: string(readmeData),
	}, nil
}

func validateRequestedPermissions(req *permission.ModuleRequest) error {
	for _, p := range append(req.Filesystem.ReadOnly, req.Filesystem.ReadWrite...) {
		trimmed := strings.TrimSpace(p)
		if trimmed == "" {
			continue
		}
		if !filepath.IsAbs(trimmed) {
			return fmt.Errorf("requested path must be absolute: %q", p)
		}
		clean := filepath.Clean(trimmed)
		if strings.Contains(clean, "..") {
			return fmt.Errorf("path traversal forbidden: %q", p)
		}
		for _, sens := range []string{"/etc", "/root", "/boot", "/sys", "/proc", "/dev"} {
			if clean == sens || strings.HasPrefix(clean, sens+"/") {
				return fmt.Errorf("access to sensitive system directory forbidden: %q", clean)
			}
		}
	}
	return nil
}

// VerifyStructure verifies module package structure and returns the main entrypoint file path.
func VerifyStructure(rootPath string) (string, error) {
	pkgInfo, err := VerifyModulePackage(rootPath)
	if err != nil {
		return "", err
	}
	return pkgInfo.MainFile, nil
}

// AuditSecurity performs deep AST static analysis on all Go files in rootPath to detect backdoors.
func AuditSecurity(rootPath string) (*AuditReport, error) {
	report := &AuditReport{
		Approved:  true,
		AuditedAt: time.Now(),
	}

	fset := token.NewFileSet()
	var scannedFiles int

	err := filepath.Walk(rootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(info.Name(), ".go") {
			return nil
		}

		scannedFiles++
		node, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return fmt.Errorf("audit: syntax error in %s: %w", info.Name(), err)
		}

		filename := filepath.Base(path)

		// 1. Audit Imports for Backdoor vectors
		for _, imp := range node.Imports {
			pathVal := strings.Trim(imp.Path.Value, `"`)
			if reason, forbidden := forbiddenImports[pathVal]; forbidden {
				report.Approved = false
				report.RiskScore += 50
				report.Violations = append(report.Violations, SecurityViolation{
					File:     filename,
					Line:     fset.Position(imp.Pos()).Line,
					Severity: "CRITICAL",
					Rule:     "FORBIDDEN_IMPORT",
					Message:  fmt.Sprintf("Import %q is blocked: %s", pathVal, reason),
				})
			}
			if pathVal == "C" {
				report.Approved = false
				report.RiskScore += 50
				report.Violations = append(report.Violations, SecurityViolation{
					File:     filename,
					Line:     fset.Position(imp.Pos()).Line,
					Severity: "CRITICAL",
					Rule:     "CGO_FORBIDDEN",
					Message:  "CGo (import \"C\") is prohibited in safe sandboxed execution",
				})
			}
		}

		// 2. Audit AST function calls and literals for suspicious patterns
		ast.Inspect(node, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				// Detect network listening / socket opening backdoors
				if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
					if pkg, ok := sel.X.(*ast.Ident); ok {
						if pkg.Name == "net" && (sel.Sel.Name == "Listen" || sel.Sel.Name == "ListenPacket") {
							report.Approved = false
							report.RiskScore += 40
							report.Violations = append(report.Violations, SecurityViolation{
								File:     filename,
								Line:     fset.Position(x.Pos()).Line,
								Severity: "CRITICAL",
								Rule:     "NETWORK_LISTENER_BACKDOOR",
								Message:  "Attempting to bind a network server/listener inside a data transformer",
							})
						}
					}
				}

			case *ast.BasicLit:
				// Detect suspicious reverse shell literals
				if x.Kind == token.STRING {
					val := strings.ToLower(x.Value)
					if strings.Contains(val, "/bin/sh") || strings.Contains(val, "/bin/bash") ||
						strings.Contains(val, "cmd.exe") || strings.Contains(val, "powershell") {
						report.Approved = false
						report.RiskScore += 30
						report.Violations = append(report.Violations, SecurityViolation{
							File:     filename,
							Line:     fset.Position(x.Pos()).Line,
							Severity: "HIGH",
							Rule:     "SHELL_LITERAL_DETECTED",
							Message:  fmt.Sprintf("Suspicious shell execution string detected: %s", x.Value),
						})
					}
				}
			}
			return true
		})

		return nil
	})

	if err != nil {
		return nil, err
	}

	report.ScannedFiles = scannedFiles
	if report.Approved {
		report.ChecksPassed = []string{
			"No forbidden system imports (os/exec, syscall, unsafe)",
			"No CGo native code execution",
			"No unauthorized network listeners or backdoors",
			"No shell execution string signatures",
			"Valid Go syntax and AST hierarchy",
		}
	}

	return report, nil
}
