package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rachmanzz/subzero/pkg/permission"
	"github.com/rachmanzz/subzero/pkg/transport"
)

// CommandProcess represents an asynchronous process running a command.
type CommandProcess interface {
	Wait() error
	Kill() error
}

// CommandRunner abstracts the container CLI executor (Docker, Podman, etc.)
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
	Start(ctx context.Context, name string, args ...string) (CommandProcess, error)
}

// OSCommandRunner executes commands via the host system's os/exec package.
type OSCommandRunner struct{}

type osCmdProcess struct {
	cmd *exec.Cmd
}

func (p *osCmdProcess) Wait() error {
	return p.cmd.Wait()
}

func (p *osCmdProcess) Kill() error {
	if p.cmd != nil && p.cmd.Process != nil {
		return p.cmd.Process.Kill()
	}
	return nil
}

func (r *OSCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.CombinedOutput()
}

func (r *OSCommandRunner) Start(ctx context.Context, name string, args ...string) (CommandProcess, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &osCmdProcess{cmd: cmd}, nil
}

// ContainerSpawnerConfig defines configuration for automated container worker management.
type ContainerSpawnerConfig struct {
	Engine        string                    // "docker" or "podman", defaults to "docker"
	Image         string                    // Container image, defaults to "subzero-worker:latest"
	ContainerName string                    // Optional custom container name
	Scheme        string                    // Transport scheme ("unix", "tcp", "http")
	Endpoint      string                    // Socket path or network address
	SecretKey     []byte                    // HMAC secret key
	IdleTimeout   time.Duration             // Time of inactivity before auto-termination
	ReadyTimeout  time.Duration             // Max time to wait for cold-start readiness
	PolicyPath    string                    // Optional path to worker permission policy file
	WorkerPolicy  *permission.WorkerPolicy  // Optional programmatic worker permission policy
	ExtraArgs     []string                  // Additional container engine flags (e.g. CPU/RAM constraints)
	Runner        CommandRunner             // Container command runner (defaults to OSCommandRunner)
}

var containerCounter uint64

// ContainerSpawner implements WorkerSpawner for on-demand container sandboxes (PRD 5.2 & 8).
type ContainerSpawner struct {
	cfg           ContainerSpawnerConfig
	tr            transport.Transport
	state         WorkerState
	proc          CommandProcess
	containerName string
	mu            sync.RWMutex
	stopped       bool
}

// NewContainerSpawner creates a ContainerSpawner instance for orchestrating worker containers.
func NewContainerSpawner(cfg ContainerSpawnerConfig) (*ContainerSpawner, error) {
	if cfg.Engine == "" {
		cfg.Engine = "docker"
	}
	if cfg.Image == "" {
		cfg.Image = "subzero-worker:latest"
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
	if cfg.Runner == nil {
		cfg.Runner = &OSCommandRunner{}
	}

	tr, err := transport.Get(cfg.Scheme)
	if err != nil {
		return nil, fmt.Errorf("container_spawner: transport get failed: %w", err)
	}

	cName := cfg.ContainerName
	if cName == "" {
		seq := atomic.AddUint64(&containerCounter, 1)
		cName = fmt.Sprintf("subzero-worker-%d-%d", time.Now().UnixNano(), seq)
	}

	return &ContainerSpawner{
		cfg:           cfg,
		tr:            tr,
		state:         StateDormant,
		containerName: cName,
	}, nil
}

// State returns the current lifecycle state of the container worker.
func (s *ContainerSpawner) State() WorkerState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

// ContainerName returns the assigned container name.
func (s *ContainerSpawner) ContainerName() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.containerName
}

// BuildRunArgs generates the CLI command arguments for running the container.
func (s *ContainerSpawner) BuildRunArgs() []string {
	args := []string{
		"run",
		"--rm",
		"--name", s.containerName,
	}

	// Environment variables
	args = append(args,
		"-e", fmt.Sprintf("SUBZERO_SCHEME=%s", s.cfg.Scheme),
		"-e", fmt.Sprintf("SUBZERO_ENDPOINT=%s", s.cfg.Endpoint),
		"-e", fmt.Sprintf("SUBZERO_SECRET=%s", string(s.cfg.SecretKey)),
		"-e", fmt.Sprintf("SUBZERO_IDLE_TIMEOUT=%s", s.cfg.IdleTimeout.String()),
	)

	if s.cfg.WorkerPolicy != nil {
		if policyBytes, err := json.Marshal(s.cfg.WorkerPolicy); err == nil {
			args = append(args, "-e", fmt.Sprintf("SUBZERO_WORKER_POLICY=%s", string(policyBytes)))
		}
	}

	// Volume / Port mappings based on transport scheme
	switch s.cfg.Scheme {
	case "unix":
		socketDir := filepath.Dir(s.cfg.Endpoint)
		args = append(args, "-v", fmt.Sprintf("%s:%s", socketDir, socketDir))
	case "tcp", "http":
		_, port, err := net.SplitHostPort(s.cfg.Endpoint)
		if err == nil && port != "" {
			args = append(args, "-p", fmt.Sprintf("%s:%s", port, port))
		}
	}

	// Append any user-provided extra engine arguments (e.g. --memory, --cpus)
	if len(s.cfg.ExtraArgs) > 0 {
		args = append(args, s.cfg.ExtraArgs...)
	}

	// Image
	args = append(args, s.cfg.Image)

	// Container Entrypoint CLI flags
	args = append(args,
		"-scheme", s.cfg.Scheme,
		"-endpoint", s.cfg.Endpoint,
		"-secret", string(s.cfg.SecretKey),
		"-idle-timeout", s.cfg.IdleTimeout.String(),
	)

	if s.cfg.PolicyPath != "" {
		args = append(args, "-policy", s.cfg.PolicyPath)
	}

	return args
}

// EnsureRunning guarantees the worker container is active. If dormant, it executes a cold-start spawn.
func (s *ContainerSpawner) EnsureRunning(ctx context.Context) error {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return ErrSpawnerStopped
	}

	if s.state == StateRunning {
		s.mu.Unlock()
		return nil
	}

	// Cold-Start Trigger: DORMANT -> PROVISIONING
	s.state = StateProvisioning

	runArgs := s.BuildRunArgs()
	proc, err := s.cfg.Runner.Start(ctx, s.cfg.Engine, runArgs...)
	if err != nil {
		s.state = StateDormant
		s.mu.Unlock()
		return fmt.Errorf("container_spawner: failed to start container (%s %s): %w", s.cfg.Engine, s.cfg.Image, err)
	}

	s.proc = proc
	s.mu.Unlock()

	// Monitor container exit in background to reset state to Dormant upon scale-to-zero auto-termination
	go func(p CommandProcess) {
		_ = p.Wait()
		s.mu.Lock()
		if s.proc == p {
			s.proc = nil
			s.state = StateDormant
		}
		s.mu.Unlock()
	}(proc)

	// Poll transport dialer until the containerized worker is ready
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
			if s.proc == proc {
				_ = proc.Kill()
				s.cleanupContainer()
				s.proc = nil
				s.state = StateDormant
			}
			s.mu.Unlock()
			return ErrColdStartTimeout
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(15 * time.Millisecond):
		}
	}
}

func (s *ContainerSpawner) cleanupContainer() {
	// Force remove container if lingering
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = s.cfg.Runner.Run(ctx, s.cfg.Engine, "rm", "-f", s.containerName)
}

// Stop terminates the container worker and cleans up container resources.
func (s *ContainerSpawner) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.stopped = true
	if s.proc != nil {
		_ = s.proc.Kill()
		s.proc = nil
	}
	s.cleanupContainer()
	s.state = StateDormant
}
