package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/rachmanzz/subzero/pkg/permission"
	"github.com/rachmanzz/subzero/pkg/transport"
)

// WorkerState represents the scale-to-zero lifecycle state.
type WorkerState string

const (
	StateDormant      WorkerState = "DORMANT"
	StateProvisioning WorkerState = "PROVISIONING"
	StateRunning      WorkerState = "RUNNING"
)

// WorkerSpawner defines the interface for on-demand worker lifecycle management.
type WorkerSpawner interface {
	State() WorkerState
	EnsureRunning(ctx context.Context) error
	Stop()
}

var (
	ErrSpawnerStopped = errors.New("spawner: supervisor is stopped")
	ErrColdStartTimeout = errors.New("spawner: cold-start worker failed to become ready in time")
)

// SpawnerConfig defines configuration for automated worker process management.
type SpawnerConfig struct {
	WorkerBinary string                    // Path to the subzero-worker executable
	Scheme       string                    // Transport scheme ("unix", "tcp", "http")
	Endpoint     string                    // Socket path or network address
	SecretKey    []byte                    // HMAC secret key
	IdleTimeout  time.Duration             // Time of inactivity before auto-termination
	ReadyTimeout time.Duration             // Max time to wait for cold-start readiness (e.g. 3s)
	PolicyPath   string                    // Optional path to worker permission policy file
	WorkerPolicy *permission.WorkerPolicy  // Optional programmatic worker permission policy
}

// ProcessSpawner manages on-demand cold-starting and scale-to-zero lifecycle for child workers.
type ProcessSpawner struct {
	cfg     SpawnerConfig
	tr      transport.Transport
	state   WorkerState
	cmd     *exec.Cmd
	mu      sync.RWMutex
	stopped bool
}

// NewProcessSpawner creates a process spawner for managing on-demand workers.
func NewProcessSpawner(cfg SpawnerConfig) (*ProcessSpawner, error) {
	if cfg.WorkerBinary == "" {
		cfg.WorkerBinary = "subzero-worker"
	}
	if cfg.Scheme == "" {
		cfg.Scheme = "unix"
	}
	if cfg.ReadyTimeout <= 0 {
		cfg.ReadyTimeout = 5 * time.Second
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = 15 * time.Second
	}

	tr, err := transport.Get(cfg.Scheme)
	if err != nil {
		return nil, fmt.Errorf("spawner: transport get failed: %w", err)
	}

	return &ProcessSpawner{
		cfg:   cfg,
		tr:    tr,
		state: StateDormant,
	}, nil
}

// State returns the current lifecycle state of the worker.
func (s *ProcessSpawner) State() WorkerState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

// EnsureRunning guarantees a worker is active. If dormant, it executes a cold-start spawn.
func (s *ProcessSpawner) EnsureRunning(ctx context.Context) error {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return ErrSpawnerStopped
	}

	if s.state == StateRunning {
		s.mu.Unlock()
		return nil
	}

	// Cold-Start Trigger
	s.state = StateProvisioning

	args := []string{
		"-scheme", s.cfg.Scheme,
		"-endpoint", s.cfg.Endpoint,
		"-secret", string(s.cfg.SecretKey),
		"-idle-timeout", s.cfg.IdleTimeout.String(),
	}

	if s.cfg.PolicyPath != "" {
		args = append(args, "-policy", s.cfg.PolicyPath)
	}

	cmd := exec.Command(s.cfg.WorkerBinary, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if s.cfg.WorkerPolicy != nil {
		if policyBytes, err := json.Marshal(s.cfg.WorkerPolicy); err == nil {
			cmd.Env = append(os.Environ(), "SUBZERO_WORKER_POLICY="+string(policyBytes))
		}
	}

	if err := cmd.Start(); err != nil {
		s.state = StateDormant
		s.mu.Unlock()
		return fmt.Errorf("spawner: failed to start worker process %q: %w", s.cfg.WorkerBinary, err)
	}

	s.cmd = cmd
	s.mu.Unlock()

	// Monitor child process exit in background to reset state to Dormant upon scale-to-zero
	go func(c *exec.Cmd) {
		_ = c.Wait()
		s.mu.Lock()
		if s.cmd == c {
			s.cmd = nil
			s.state = StateDormant
		}
		s.mu.Unlock()
	}(cmd)

	// Wait until worker is ready by polling the transport dialer
	readyDeadline := time.Now().Add(s.cfg.ReadyTimeout)
	for {
		dialCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		conn, err := s.tr.Dial(dialCtx, s.cfg.Endpoint)
		cancel()

		if err == nil {
			_ = conn.Close()
			s.mu.Lock()
			s.state = StateRunning
			s.mu.Unlock()
			return nil
		}

		if time.Now().After(readyDeadline) {
			s.mu.Lock()
			if s.cmd == cmd {
				_ = cmd.Process.Kill()
				s.state = StateDormant
			}
			s.mu.Unlock()
			return ErrColdStartTimeout
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// Stop terminates any running worker child process.
func (s *ProcessSpawner) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.stopped = true
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		s.cmd = nil
	}
	s.state = StateDormant
}
