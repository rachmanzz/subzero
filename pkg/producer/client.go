package producer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rachmanzz/subzero/internal/storage"
	"github.com/rachmanzz/subzero/pkg/protocol"
	"github.com/rachmanzz/subzero/pkg/transport"
)

var (
	ErrNilTask      = errors.New("producer: task message cannot be nil")
	ErrEmptyEndpoint = errors.New("producer: endpoint cannot be empty")
)

// Config configures the Producer client.
type Config struct {
	Scheme        string        // Transport scheme ("unix", "tcp", "http", "inproc")
	Endpoint      string        // Target socket path or network address
	SecretKey     []byte        // HMAC-SHA256 signature key
	Timeout       time.Duration // Network request timeout
	StorageDriver storage.Driver
}

// Client provides an API SDK for Main API Gateways to interface with Subzero.
type Client struct {
	cfg Config
	tr  transport.Transport
}

// New creates a new Subzero Producer client.
func New(cfg Config) (*Client, error) {
	if cfg.Scheme == "" {
		cfg.Scheme = "unix"
	}
	if cfg.Endpoint == "" {
		return nil, ErrEmptyEndpoint
	}
	if len(cfg.SecretKey) == 0 {
		return nil, protocol.ErrEmptySecretKey
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}

	tr, err := transport.Get(cfg.Scheme)
	if err != nil {
		return nil, fmt.Errorf("producer: transport error: %w", err)
	}

	return &Client{
		cfg: cfg,
		tr:  tr,
	}, nil
}

// RequestUploadSlot allocates a pre-signed direct upload slot using the configured storage driver.
func (c *Client) RequestUploadSlot(ctx context.Context, storageRef string, ttl time.Duration) (*protocol.UploadSlotResponse, error) {
	if c.cfg.StorageDriver == nil {
		return nil, errors.New("producer: storage driver not configured")
	}
	return c.cfg.StorageDriver.GenerateUploadSlot(ctx, storageRef, ttl)
}

// SubmitTask sends an authenticated TaskMessage over the transport and awaits execution ResultMessage.
func (c *Client) SubmitTask(ctx context.Context, task *protocol.TaskMessage) (*protocol.ResultMessage, error) {
	if task == nil {
		return nil, ErrNilTask
	}

	dialCtx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	conn, err := c.tr.Dial(dialCtx, c.cfg.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("producer: dial failed: %w", err)
	}
	defer conn.Close()

	taskBytes, err := protocol.EncodeTask(task, c.cfg.SecretKey)
	if err != nil {
		return nil, fmt.Errorf("producer: encode task failed: %w", err)
	}

	if err := protocol.WriteFrame(conn, taskBytes, c.cfg.SecretKey); err != nil {
		return nil, fmt.Errorf("producer: write frame failed: %w", err)
	}

	resBytes, err := protocol.ReadFrame(conn, c.cfg.SecretKey, 0)
	if err != nil {
		return nil, fmt.Errorf("producer: read result frame failed: %w", err)
	}

	result, err := protocol.DecodeResult(resBytes)
	if err != nil {
		return nil, fmt.Errorf("producer: decode result failed: %w", err)
	}

	return result, nil
}
