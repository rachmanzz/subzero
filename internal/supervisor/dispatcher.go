package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"

	"github.com/rachmanzz/fiber-extras/v3/queue"
	"github.com/rachmanzz/fiber-extras/v3/worker"
	"github.com/rachmanzz/subzero/pkg/protocol"
)

var (
	ErrDispatcherClosed = errors.New("dispatcher: engine is closed")
	ErrQueueFull        = errors.New("dispatcher: task queue is full")
	ErrNilTask          = errors.New("dispatcher: cannot dispatch nil task")
)

// Task defines an executable unit of work handled by the Dispatcher (aliases fiber-extras/v3/worker.Task).
type Task = worker.Task

// SubTaskFunc represents a sub-task function executed concurrently by a sub-worker.
type SubTaskFunc = worker.SubTaskFunc

// SubWorkerPool provides a fan-out mechanism to run sub-tasks in parallel within a parent task.
type SubWorkerPool = worker.SubWorkerPool

// NewTaskFunc creates a simple Task implementation wrapping a function.
func NewTaskFunc(name string, run func(ctx context.Context, pool SubWorkerPool) error) Task {
	return worker.NewTaskFunc(name, run)
}

// DispatcherConfig configures the task dispatcher engine.
type DispatcherConfig struct {
	Concurrency int          // Maximum number of concurrent tasks (throttling semaphore)
	QueueSize   int          // Maximum in-memory task queue buffer
	SubLimit    int          // Maximum parallel sub-workers per task
	QueueDriver queue.Driver // Optional persistent queue driver from fiber-extras/v3/queue (e.g. MemoryDriver, Redis, NATS)
	QueueRouter *queue.Router
}

// DefaultDispatcherConfig returns sensible defaults.
func DefaultDispatcherConfig() DispatcherConfig {
	return DispatcherConfig{
		Concurrency: 16,
		QueueSize:   1024,
		SubLimit:    8,
	}
}

// Dispatcher coordinates task queueing, throttling, panic recovery, sub-worker fan-out, and queue consumption.
type Dispatcher struct {
	cfg           DispatcherConfig
	taskQueue     chan Task
	semaphore     chan struct{}
	wg            sync.WaitGroup
	quit          chan struct{}
	queueRouter   *queue.Router
	queueConsumer *queue.Consumer
	started       bool
	closed        bool
	mu            sync.RWMutex
}

// NewDispatcher creates a new Dispatcher with the given configuration.
func NewDispatcher(cfg DispatcherConfig) *Dispatcher {
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 16
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 1024
	}
	if cfg.SubLimit <= 0 {
		cfg.SubLimit = 8
	}

	d := &Dispatcher{
		cfg:       cfg,
		taskQueue: make(chan Task, cfg.QueueSize),
		semaphore: make(chan struct{}, cfg.Concurrency),
		quit:      make(chan struct{}),
	}

	if cfg.QueueDriver != nil {
		router := cfg.QueueRouter
		if router == nil {
			router = queue.NewRouter()
		}
		d.queueRouter = router
		d.queueConsumer = queue.NewConsumer(cfg.QueueDriver, router, d)
	}

	return d
}

// Start boots the background worker goroutines and starts the queue consumer if configured.
func (d *Dispatcher) Start() {
	d.mu.Lock()
	if d.started || d.closed {
		d.mu.Unlock()
		return
	}
	d.started = true
	consumer := d.queueConsumer
	d.mu.Unlock()

	for i := 0; i < d.cfg.Concurrency; i++ {
		d.wg.Add(1)
		go d.workerLoop()
	}

	if consumer != nil {
		consumer.Start()
	}
}

func (d *Dispatcher) workerLoop() {
	defer d.wg.Done()

	for {
		select {
		case <-d.quit:
			return
		case task, ok := <-d.taskQueue:
			if !ok {
				return
			}
			d.runTaskSafely(context.Background(), task)
		}
	}
}

func (d *Dispatcher) runTaskSafely(ctx context.Context, task Task) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("dispatcher: panic recovered in task %q: %v\nstack: %s", task.Name(), r, debug.Stack())
		}
	}()

	pool := newSubWorkerPool(ctx, d.cfg.SubLimit)
	err = task.Execute(ctx, pool)
	if waitErr := pool.Wait(); waitErr != nil && err == nil {
		err = waitErr
	}
	return err
}

// Dispatch enqueues a task for asynchronous execution (non-blocking).
func (d *Dispatcher) Dispatch(ctx context.Context, task Task) error {
	if task == nil {
		return ErrNilTask
	}

	d.mu.RLock()
	defer d.mu.RUnlock()

	if d.closed {
		return ErrDispatcherClosed
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-d.quit:
		return ErrDispatcherClosed
	case d.taskQueue <- task:
		return nil
	default:
		return ErrQueueFull
	}
}

// ExecuteSync runs a task synchronously, respecting throttling and sub-worker pools.
func (d *Dispatcher) ExecuteSync(ctx context.Context, task Task) error {
	if task == nil {
		return ErrNilTask
	}

	d.mu.RLock()
	if d.closed {
		d.mu.RUnlock()
		return ErrDispatcherClosed
	}
	d.mu.RUnlock()

	// Acquire semaphore slot
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-d.quit:
		return ErrDispatcherClosed
	case d.semaphore <- struct{}{}:
	}

	defer func() {
		<-d.semaphore
	}()

	return d.runTaskSafely(ctx, task)
}

// RegisterQueueHandler registers a topic handler on the persistent queue.
// When messages arrive on the topic, they are decoded into protocol.TaskMessage and executed with SubWorkerPool.
func (d *Dispatcher) RegisterQueueHandler(topic string, handler func(ctx context.Context, task *protocol.TaskMessage, pool SubWorkerPool) error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.queueRouter == nil {
		d.queueRouter = queue.NewRouter()
		if d.cfg.QueueDriver != nil && d.queueConsumer == nil {
			d.queueConsumer = queue.NewConsumer(d.cfg.QueueDriver, d.queueRouter, d)
		}
	}

	d.queueRouter.Register(topic, func(ctx context.Context, msg *queue.Message, pool worker.SubWorkerPool) error {
		var task protocol.TaskMessage
		if err := json.Unmarshal(msg.Payload, &task); err != nil {
			return fmt.Errorf("dispatcher: decode queue task failed: %w", err)
		}
		return handler(ctx, &task, pool)
	})
}

// Stop gracefully shuts down the dispatcher and stops any active queue consumer.
func (d *Dispatcher) Stop() {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.closed = true
	close(d.quit)
	close(d.taskQueue)
	consumer := d.queueConsumer
	d.mu.Unlock()

	if consumer != nil {
		consumer.Stop()
	}

	d.wg.Wait()
}

// subWorkerPool implements SubWorkerPool with concurrency limiting.
type subWorkerPool struct {
	ctx      context.Context
	cancel   context.CancelFunc
	sem      chan struct{}
	wg       sync.WaitGroup
	errOnce  sync.Once
	firstErr error
}

func newSubWorkerPool(parent context.Context, limit int) *subWorkerPool {
	ctx, cancel := context.WithCancel(parent)
	return &subWorkerPool{
		ctx:    ctx,
		cancel: cancel,
		sem:    make(chan struct{}, limit),
	}
}

func (p *subWorkerPool) Go(fn SubTaskFunc) {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()

		select {
		case <-p.ctx.Done():
			return
		case p.sem <- struct{}{}:
		}
		defer func() { <-p.sem }()

		if err := fn(p.ctx); err != nil {
			p.errOnce.Do(func() {
				p.firstErr = err
				p.cancel()
			})
		}
	}()
}

func (p *subWorkerPool) Wait() error {
	p.wg.Wait()
	p.cancel()
	return p.firstErr
}
