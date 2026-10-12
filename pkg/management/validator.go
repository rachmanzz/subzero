package management

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"time"
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

// VerifyStructure inspects the unpacked directory to ensure it is a valid Go program with main().
func VerifyStructure(rootPath string) (string, error) {
	var mainFile string
	hasMainFunc := false

	fset := token.NewFileSet()

	err := filepath.Walk(rootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(info.Name(), ".go") || strings.HasSuffix(info.Name(), "_test.go") {
			return nil
		}

		node, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return fmt.Errorf("structure: syntax error in %s: %w", info.Name(), err)
		}

		if node.Name.Name == "main" {
			for _, decl := range node.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "main" {
					hasMainFunc = true
					mainFile = path
					break
				}
			}
		}
		return nil
	})

	if err != nil {
		return "", err
	}

	if !hasMainFunc {
		return "", ErrMissingMainEntrypoint
	}

	return mainFile, nil
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
