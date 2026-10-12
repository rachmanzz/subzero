package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/rachmanzz/subzero/pkg/protocol"
)

// HandlerFunc defines the business logic execution signature for processing a TaskMessage.
type HandlerFunc func(ctx context.Context, task *protocol.TaskMessage) (*protocol.ResultMessage, error)

// Config configures a Subzero Worker instance.
type Config struct {
	SecretKey   []byte        // HMAC-SHA256 authentication key
	IdleTimeout time.Duration // Time of inactivity before triggering auto-termination
	OnTerminate func()        // Callback invoked on idle timeout or graceful termination
}

// Worker represents an on-demand workload execution unit.
type Worker struct {
	cfg      Config
	handler  HandlerFunc
	watchdog *Watchdog
	listener net.Listener
	closed   bool
	mu       sync.Mutex
	wg       sync.WaitGroup
}

// New creates a new Worker.
func New(cfg Config, handler HandlerFunc) *Worker {
	w := &Worker{
		cfg:     cfg,
		handler: handler,
	}

	w.watchdog = NewWatchdog(cfg.IdleTimeout, func() {
		_ = w.Close()
		if cfg.OnTerminate != nil {
			cfg.OnTerminate()
		}
	})

	return w
}

// Serve begins accepting and processing framed tasks from listener.
func (w *Worker) Serve(l net.Listener) error {
	w.mu.Lock()
	w.listener = l
	w.mu.Unlock()

	w.watchdog.Start()

	for {
		conn, err := l.Accept()
		if err != nil {
			w.mu.Lock()
			isClosed := w.closed
			w.mu.Unlock()
			if isClosed {
				return nil
			}
			return fmt.Errorf("worker: accept failed: %w", err)
		}

		w.wg.Add(1)
		go func(c net.Conn) {
			defer w.wg.Done()
			w.handleConn(c)
		}(conn)
	}
}

func (w *Worker) handleConn(conn net.Conn) {
	defer conn.Close()

	for {
		rawPayload, err := protocol.ReadFrame(conn, w.cfg.SecretKey, 0)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return
			}
			return
		}

		task, err := protocol.DecodeTask(rawPayload)
		if err != nil {
			// Write back error result
			errResult := &protocol.ResultMessage{
				IsSuccess:    false,
				ErrorMessage: fmt.Sprintf("invalid task payload: %v", err),
			}
			data, _ := protocol.EncodeResult(errResult)
			_ = protocol.WriteFrame(conn, data, w.cfg.SecretKey)
			return
		}

		w.watchdog.ActivityStarted()
		start := time.Now()

		var result *protocol.ResultMessage
		if w.handler != nil {
			result, err = w.handler(context.Background(), task)
		} else {
			result = &protocol.ResultMessage{
				TaskID:    task.TaskID,
				IsSuccess: true,
			}
		}

		elapsed := time.Since(start)
		w.watchdog.ActivityFinished()

		if err != nil && result == nil {
			result = &protocol.ResultMessage{
				TaskID:        task.TaskID,
				IsSuccess:     false,
				ErrorMessage:  err.Error(),
				ExecutionTime: elapsed.String(),
			}
		} else if result != nil {
			result.ExecutionTime = elapsed.String()
		}

		resBytes, err := protocol.EncodeResult(result)
		if err != nil {
			return
		}

		if err := protocol.WriteFrame(conn, resBytes, w.cfg.SecretKey); err != nil {
			return
		}
	}
}

// Close gracefully stops the worker and closes the underlying listener.
func (w *Worker) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	w.watchdog.Stop()
	var err error
	if w.listener != nil {
		err = w.listener.Close()
	}
	w.mu.Unlock()

	w.wg.Wait()
	return err
}
