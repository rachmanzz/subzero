package management

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/rachmanzz/subzero/pkg/management/ui"
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

		api.Route("/modules", func(mr chi.Router) {
			mr.Get("/", s.handleListModules)
			mr.Post("/compile", s.handleCompileModule)
			mr.Get("/{id}/download", s.handleDownloadWasm)
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
		"engine":          "subzero",
		"status":          "online",
		"compiler":        s.cfg.Compiler.Name(),
		"modules_count":   len(modules),
		"scale_to_zero":   true,
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

	if !strings.HasSuffix(header.Filename, ".go") {
		http.Error(w, "file must be a .go source file", http.StatusBadRequest)
		return
	}

	sourceBytes, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "failed to read uploaded file", http.StatusInternalServerError)
		return
	}

	tenantID := r.FormValue("tenant_id")
	if tenantID == "" {
		tenantID = "default"
	}

	moduleName := r.FormValue("module_name")
	if moduleName == "" {
		moduleName = strings.TrimSuffix(header.Filename, ".go")
	}

	targetOS := r.FormValue("target_os")
	if targetOS == "" {
		targetOS = "wasip1"
	}

	opts := CompileOptions{
		TenantID:   tenantID,
		ModuleName: moduleName,
		TargetOS:   targetOS,
		Optimize:   true,
	}

	compileRes, err := s.cfg.Compiler.Compile(r.Context(), sourceBytes, opts)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success":      false,
			"error":        err.Error(),
			"compiler_log": compileRes.CompilerLog,
		})
		return
	}

	moduleID := fmt.Sprintf("%s-%s-%d", tenantID, moduleName, time.Now().Unix())
	mod := &WasmModule{
		ID:          moduleID,
		TenantID:    tenantID,
		Name:        moduleName,
		Target:      targetOS,
		CreatedAt:   time.Now(),
		CompileTime: compileRes.Duration.String(),
	}

	if err := s.cfg.Store.Save(r.Context(), mod, compileRes.WasmBytes, sourceBytes); err != nil {
		http.Error(w, "failed to save compiled wasm artifact: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":      true,
		"module_id":    moduleID,
		"size":         compileRes.Size,
		"duration":     compileRes.Duration.String(),
		"compiler_log": compileRes.CompilerLog,
	})
}

func (s *Server) handleDownloadWasm(w http.ResponseWriter, r *http.Request) {
	moduleID := chi.URLParam(r, "id")
	wasmBytes, err := s.cfg.Store.GetWasmBytes(r.Context(), moduleID)
	if err != nil {
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
		http.Error(w, "failed to delete module: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
