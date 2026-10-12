package producer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/rachmanzz/fiber-extras/v3/queue"
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
	QueueDriver   queue.Driver  // Optional distributed queue driver (fiber-extras/v3/queue)
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
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}

	var tr transport.Transport
	if cfg.Endpoint != "" {
		if len(cfg.SecretKey) == 0 {
			return nil, protocol.ErrEmptySecretKey
		}
		var err error
		tr, err = transport.Get(cfg.Scheme)
		if err != nil {
			return nil, fmt.Errorf("producer: transport error: %w", err)
		}
	} else if cfg.QueueDriver == nil {
		return nil, ErrEmptyEndpoint
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
	if c.tr == nil || c.cfg.Endpoint == "" {
		return nil, errors.New("producer: direct RPC transport endpoint not configured")
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

// EnqueueTask enqueues a TaskMessage into the configured queue driver.
// This allows Main API Gateways to submit tasks asynchronously to the distributed queue broker.
func (c *Client) EnqueueTask(ctx context.Context, topic string, task *protocol.TaskMessage, opts ...queue.DispatchOption) error {
	if task == nil {
		return ErrNilTask
	}
	if topic == "" {
		return errors.New("producer: topic cannot be empty")
	}
	if c.cfg.QueueDriver == nil {
		return errors.New("producer: queue driver not configured")
	}

	payload, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("producer: marshal task failed: %w", err)
	}

	msg := queue.NewMessage(topic, payload, opts...)
	return c.cfg.QueueDriver.Enqueue(ctx, msg)
}

// EnqueueWithDriver enqueues a TaskMessage using an explicitly provided queue driver.
func (c *Client) EnqueueWithDriver(ctx context.Context, driver queue.Driver, topic string, task *protocol.TaskMessage, opts ...queue.DispatchOption) error {
	if task == nil {
		return ErrNilTask
	}
	if driver == nil {
		return errors.New("producer: queue driver cannot be nil")
	}
	if topic == "" {
		return errors.New("producer: topic cannot be empty")
	}

	payload, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("producer: marshal task failed: %w", err)
	}

	msg := queue.NewMessage(topic, payload, opts...)
	return driver.Enqueue(ctx, msg)
}

