package supervisor_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rachmanzz/subzero/internal/supervisor"
)

type mockTask struct {
	name string
	fn   func(ctx context.Context, pool supervisor.SubWorkerPool) error
}

func (m *mockTask) Name() string { return m.name }
func (m *mockTask) Execute(ctx context.Context, pool supervisor.SubWorkerPool) error {
	return m.fn(ctx, pool)
}

func TestDispatcherSyncExecution(t *testing.T) {
	d := supervisor.NewDispatcher(supervisor.DispatcherConfig{
		Concurrency: 4,
		QueueSize:   16,
	})
	defer d.Stop()

	var executed bool
	task := &mockTask{
		name: "sync-task",
		fn: func(ctx context.Context, pool supervisor.SubWorkerPool) error {
			executed = true
			return nil
		},
	}

	err := d.ExecuteSync(context.Background(), task)
	if err != nil {
		t.Fatalf("ExecuteSync failed: %v", err)
	}
	if !executed {
		t.Fatal("task was not executed")
	}
}

func TestDispatcherPanicRecovery(t *testing.T) {
	d := supervisor.NewDispatcher(supervisor.DispatcherConfig{
		Concurrency: 4,
		QueueSize:   16,
	})
	defer d.Stop()

	panicTask := &mockTask{
		name: "panic-task",
		fn: func(ctx context.Context, pool supervisor.SubWorkerPool) error {
			panic("something exploded!")
		},
	}

	err := d.ExecuteSync(context.Background(), panicTask)
	if err == nil {
		t.Fatal("expected panic to be recovered and returned as error, got nil")
	}
}

func TestDispatcherSubWorkerFanOut(t *testing.T) {
	d := supervisor.NewDispatcher(supervisor.DispatcherConfig{
		Concurrency: 4,
		QueueSize:   16,
		SubLimit:    4,
	})
	defer d.Stop()

	var counter int64
	fanOutTask := &mockTask{
		name: "fan-out-task",
		fn: func(ctx context.Context, pool supervisor.SubWorkerPool) error {
			for i := 0; i < 5; i++ {
				pool.Go(func(ctx context.Context) error {
					atomic.AddInt64(&counter, 1)
					return nil
				})
			}
			return nil
		},
	}

	err := d.ExecuteSync(context.Background(), fanOutTask)
	if err != nil {
		t.Fatalf("fan-out task failed: %v", err)
	}

	if atomic.LoadInt64(&counter) != 5 {
		t.Fatalf("expected counter to be 5, got %d", atomic.LoadInt64(&counter))
	}
}

func TestDispatcherAsyncQueue(t *testing.T) {
	d := supervisor.NewDispatcher(supervisor.DispatcherConfig{
		Concurrency: 2,
		QueueSize:   16,
	})
	d.Start()
	defer d.Stop()

	doneCh := make(chan struct{})
	asyncTask := &mockTask{
		name: "async-task",
		fn: func(ctx context.Context, pool supervisor.SubWorkerPool) error {
			close(doneCh)
			return nil
		},
	}

	err := d.Dispatch(context.Background(), asyncTask)
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}

	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("async task timed out")
	}
}

func TestDispatcherSubWorkerErrorPropagation(t *testing.T) {
	d := supervisor.NewDispatcher(supervisor.DispatcherConfig{
		Concurrency: 2,
		QueueSize:   16,
	})
	defer d.Stop()

	expectedErr := errors.New("sub-worker calculation failed")
	task := &mockTask{
		name: "failing-sub-worker",
		fn: func(ctx context.Context, pool supervisor.SubWorkerPool) error {
			pool.Go(func(ctx context.Context) error {
				return expectedErr
			})
			return nil
		},
	}

	err := d.ExecuteSync(context.Background(), task)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected error %v, got %v", expectedErr, err)
	}
}
