// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/rachmanzz/subzero/pkg/management"
)

func main() {
	port := flag.Int("port", 8080, "Port for the Subzero Management API & Dashboard")
	dataDir := flag.String("data-dir", filepath.Join(".", "data", "wasm"), "Directory to store compiled Wasm artifacts")
	compilerBin := flag.String("compiler-bin", "go", "Path to Go binary for Wasm compilation")
	flag.Parse()

	log.Printf("[Subzero Admin] Initializing management engine on port %d...", *port)

	store, err := management.NewFileStore(*dataDir)
	if err != nil {
		log.Fatalf("[Subzero Admin] Failed to initialize file store: %v", err)
	}

	comp := management.NewGoCompiler(*compilerBin)

	server, err := management.NewServer(management.ServerConfig{
		Compiler: comp,
		Store:    store,
	})
	if err != nil {
		log.Fatalf("[Subzero Admin] Failed to initialize server: %v", err)
	}

	addr := fmt.Sprintf(":%d", *port)
	httpServer := &http.Server{
		Addr:         addr,
		Handler:      server.Routes(),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
	}

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Println("[Subzero Admin] Shutdown signal received. Exiting...")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(ctx)
	}()

	log.Printf("[Subzero Admin] Dashboard UI ready at: http://localhost:%d", *port)
	log.Printf("[Subzero Admin] Storing Wasm modules in: %s", *dataDir)

	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("[Subzero Admin] Server failed: %v", err)
	}
}
