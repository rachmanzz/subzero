package worker

import (
	"sync"
	"time"
)

// Watchdog monitors idle periods and triggers auto-termination when the timeout window elapses.
type Watchdog struct {
	timeout   time.Duration
	onTimeout func()
	timer     *time.Timer
	active    int64
	mu        sync.Mutex
	stopped   bool
}

// NewWatchdog creates an idle watchdog. onTimeout is called when no activity occurs within timeout duration.
func NewWatchdog(timeout time.Duration, onTimeout func()) *Watchdog {
	w := &Watchdog{
		timeout:   timeout,
		onTimeout: onTimeout,
	}
	return w
}

// Start begins the idle countdown.
func (w *Watchdog) Start() {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.stopped || w.timeout <= 0 {
		return
	}

	w.resetTimerLocked()
}

// ActivityStarted signals that a workload has begun execution (pausing the idle countdown).
func (w *Watchdog) ActivityStarted() {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.active++
	if w.timer != nil {
		w.timer.Stop()
	}
}

// ActivityFinished signals that a workload completed. If no workloads remain active, countdown restarts.
func (w *Watchdog) ActivityFinished() {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.active > 0 {
		w.active--
	}

	if w.active == 0 && !w.stopped && w.timeout > 0 {
		w.resetTimerLocked()
	}
}

func (w *Watchdog) resetTimerLocked() {
	if w.timer != nil {
		w.timer.Stop()
	}

	w.timer = time.AfterFunc(w.timeout, func() {
		w.mu.Lock()
		if w.stopped || w.active > 0 {
			w.mu.Unlock()
			return
		}
		w.stopped = true
		w.mu.Unlock()

		if w.onTimeout != nil {
			w.onTimeout()
		}
	})
}

// Stop permanently stops the watchdog.
func (w *Watchdog) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.stopped = true
	if w.timer != nil {
		w.timer.Stop()
	}
}
