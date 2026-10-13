package management

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/rachmanzz/subzero/pkg/management/ui"
	"github.com/rachmanzz/subzero/pkg/permission"
)

// ServerConfig configures the Subzero management API & dashboard.
type ServerConfig struct {
	Compiler Compiler
	Store    Store
}

// Server provides the pluggable Chi-based Subzero Management & Dashboard engine.
type Server struct {
	cfg    ServerConfig
	router chi.Router
}

// NewServer creates a new Management Server instance with pluggable compiler and store adapters.
func NewServer(cfg ServerConfig) (*Server, error) {
	if cfg.Compiler == nil {
		cfg.Compiler = NewGoCompiler("")
	}
	if cfg.Store == nil {
		store, err := NewFileStore(filepath.Join(".", "data", "wasm"))
		if err != nil {
			return nil, fmt.Errorf("management: failed to initialize default store: %w", err)
		}
		cfg.Store = store
	}

	s := &Server{
		cfg: cfg,
	}
	s.setupRoutes()
	return s, nil
}

// Routes returns the configured Chi router (useful for mounting into existing Chi applications).
func (s *Server) Routes() chi.Router {
	return s.router
}

func (s *Server) setupRoutes() {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(60 * time.Second))

	// Dashboard UI
	r.Get("/", s.handleDashboard)

	// API endpoints
	r.Route("/api", func(api chi.Router) {
		api.Get("/status", s.handleStatus)
		api.Post("/inspect-policy", s.handleInspectPolicyDirect)

		api.Route("/modules", func(mr chi.Router) {
			mr.Get("/", s.handleListModules)
			mr.Post("/compile", s.handleCompileModule)
			mr.Get("/{id}/download", s.handleDownloadWasm)
			mr.Post("/{id}/inspect-policy", s.handleInspectModulePolicy)
			mr.Delete("/{id}", s.handleDeleteModule)
		})
	})

	s.router = r
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(ui.DashboardHTML)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	modules, _ := s.cfg.Store.List(r.Context(), "")
	status := map[string]any{
		"engine":        "subzero",
		"status":        "online",
		"compiler":      s.cfg.Compiler.Name(),
		"modules_count": len(modules),
		"scale_to_zero": true,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(status)
}

func (s *Server) handleListModules(w http.ResponseWriter, r *http.Request) {
	tenantID := r.URL.Query().Get("tenant_id")
	modules, err := s.cfg.Store.List(r.Context(), tenantID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(modules)
}

func (s *Server) handleCompileModule(w http.ResponseWriter, r *http.Request) {
	// 32MB max upload memory
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "invalid multipart form", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "missing 'file' field", http.StatusBadRequest)
		return
	}
	defer file.Close()

	filename := strings.ToLower(header.Filename)
	isTarGz := strings.HasSuffix(filename, ".tar.gz") || strings.HasSuffix(filename, ".tgz")

	if !isTarGz {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"stage":   "structure_verification",
			"error":   "uploaded file must be a .tar.gz bundle containing config.zero, readme.md, and your Go main entrypoint",
		})
		return
	}

	// Create isolated workspace for unpack and verification
	unpackDir, err := os.MkdirTemp("", "subzero-pkg-verify-*")
	if err != nil {
		http.Error(w, "failed to create workspace: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer os.RemoveAll(unpackDir)

	if err := SafeUnpackTarGz(file, unpackDir, DefaultArchiveLimits()); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"stage":   "archive_unpack",
			"error":   "failed to unpack archive safely: " + err.Error(),
		})
		return
	}

	// 1. Structure Verification: Ensure config.zero, readme.md, and main entrypoint exist
	pkgInfo, err := VerifyModulePackage(unpackDir)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"stage":   "structure_verification",
			"error":   err.Error(),
		})
		return
	}

	mainFile := pkgInfo.MainFile
	sourceBytes, _ := os.ReadFile(mainFile)

	// 2. Security & Backdoor Static Analysis (AST scan)
	auditReport, err := AuditSecurity(pkgInfo.RootPath)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"stage":   "security_audit",
			"error":   "security static analysis error: " + err.Error(),
		})
		return
	}

	if !auditReport.Approved {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"stage":   "security_audit",
			"error":   "Security validation failed: potential backdoor or forbidden imports detected",
			"audit":   auditReport,
		})
		return
	}

	// 3. Compilation Phase
	tenantID := r.FormValue("tenant_id")
	if tenantID == "" && pkgInfo.Config.Tenant != "" {
		tenantID = pkgInfo.Config.Tenant
	}
	if tenantID == "" {
		tenantID = "default"
	}

	moduleName := r.FormValue("module_name")
	if moduleName == "" && pkgInfo.Config.Name != "" {
		moduleName = pkgInfo.Config.Name
	}
	if moduleName == "" {
		cleanBase := filepath.Base(header.Filename)
		cleanBase = strings.TrimSuffix(cleanBase, ".tar.gz")
		cleanBase = strings.TrimSuffix(cleanBase, ".tgz")
		moduleName = cleanBase
	}

	targetOS := r.FormValue("target_os")
	if targetOS == "" && pkgInfo.Config.Target != "" {
		targetOS = pkgInfo.Config.Target
	}
	if targetOS == "" {
		targetOS = "wasip1"
	}

	opts := CompileOptions{
		TenantID:   tenantID,
		ModuleName: moduleName,
		TargetOS:   targetOS,
		Optimize:   true,
	}

	var compileRes *CompileResult
	if goComp, ok := s.cfg.Compiler.(*GoCompiler); ok {
		compileRes, err = goComp.CompileDir(r.Context(), pkgInfo.RootPath, opts)
	} else {
		compileRes, err = s.cfg.Compiler.Compile(r.Context(), sourceBytes, opts)
	}

	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success":      false,
			"stage":        "compilation",
			"error":        err.Error(),
			"compiler_log": compileRes.CompilerLog,
		})
		return
	}

	// 4. Publication Phase
	moduleID := fmt.Sprintf("%s-%s-%d", tenantID, moduleName, time.Now().Unix())
	mod := &WasmModule{
		ID:          moduleID,
		TenantID:    tenantID,
		Name:        moduleName,
		Target:      targetOS,
		Status:      "PUBLISHED",
		Audit:       auditReport,
		Config:      pkgInfo.Config,
		Readme:      pkgInfo.ReadmeContent,
		CreatedAt:   time.Now(),
		CompileTime: compileRes.Duration.String(),
	}

	if err := s.cfg.Store.Save(r.Context(), mod, compileRes.WasmBytes, sourceBytes); err != nil {
		http.Error(w, "failed to publish wasm artifact: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":      true,
		"published":    true,
		"module_id":    moduleID,
		"size":         compileRes.Size,
		"duration":     compileRes.Duration.String(),
		"audit":        auditReport,
		"compiler_log": compileRes.CompilerLog,
	})
}

func (s *Server) handleDownloadWasm(w http.ResponseWriter, r *http.Request) {
	moduleID := chi.URLParam(r, "id")
	wasmBytes, err := s.cfg.Store.GetWasmBytes(r.Context(), moduleID)
	if err != nil {
		if errors.Is(err, ErrInvalidModuleID) {
			http.Error(w, "invalid module id: path traversal forbidden", http.StatusBadRequest)
			return
		}
		http.Error(w, "wasm module not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/wasm")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.wasm"`, moduleID))
	_, _ = w.Write(wasmBytes)
}

func (s *Server) handleDeleteModule(w http.ResponseWriter, r *http.Request) {
	moduleID := chi.URLParam(r, "id")
	if err := s.cfg.Store.Delete(r.Context(), moduleID); err != nil {
		if errors.Is(err, ErrInvalidModuleID) {
			http.Error(w, "invalid module id: path traversal forbidden", http.StatusBadRequest)
			return
		}
		http.Error(w, "failed to delete module: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleInspectPolicyDirect(w http.ResponseWriter, r *http.Request) {
	var req InspectPolicyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	workerPolicy := req.WorkerPolicy
	if workerPolicy == nil {
		defPolicy := permission.DefaultWorkerPolicy()
		workerPolicy = &defPolicy
	}

	eff, err := permission.Evaluate(workerPolicy, req.ModuleRequest)
	resp := InspectPolicyResponse{
		Allowed:              err == nil,
		RequestedPermissions: req.ModuleRequest,
		EffectivePermissions: eff,
	}
	if err != nil {
		resp.Error = err.Error()
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleInspectModulePolicy(w http.ResponseWriter, r *http.Request) {
	moduleID := chi.URLParam(r, "id")
	mod, err := s.cfg.Store.Get(r.Context(), moduleID)
	if err != nil {
		if errors.Is(err, ErrInvalidModuleID) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid module id: path traversal forbidden"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "wasm module not found"})
		return
	}

	var req InspectPolicyRequest
	if r.Body != nil && r.ContentLength > 0 {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}

	workerPolicy := req.WorkerPolicy
	if workerPolicy == nil {
		defPolicy := permission.DefaultWorkerPolicy()
		workerPolicy = &defPolicy
	}

	moduleReq := req.ModuleRequest
	if moduleReq == nil && mod.Config != nil && mod.Config.Permissions != nil {
		moduleReq = mod.Config.Permissions
	}

	eff, err := permission.Evaluate(workerPolicy, moduleReq)
	resp := InspectPolicyResponse{
		ModuleID:             moduleID,
		TenantID:             mod.TenantID,
		Allowed:              err == nil,
		RequestedPermissions: moduleReq,
		EffectivePermissions: eff,
	}
	if err != nil {
		resp.Error = err.Error()
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
