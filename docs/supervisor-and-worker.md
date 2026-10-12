# Supervisor, Concurrency Dispatcher & Worker Lifecycle

This document describes the orchestration layer of Subzero: the **Supervisor** (host daemon and task coordinator), the **Concurrency Dispatcher** (`v3/worker` pattern), and the **Scale-to-Zero Worker** lifecycle.

---

## 1. Concurrency Dispatcher Engine

Located in [`internal/supervisor/dispatcher.go`](../internal/supervisor/dispatcher.go), the Dispatcher handles task queueing and execution using an architecture inspired by the high-performance [`v3/worker`](https://github.com/rachmanzz/fiber-extras/tree/main/v3/worker) engine from the [rachmanzz/fiber-extras](https://github.com/rachmanzz/fiber-extras) ecosystem.

> **Note on Upstream Origin**:
> * `v3/worker`: Extracted from the `fiber-extras` project, providing an in-memory worker pool with sub-worker fan-out and panic recovery.
> * `v3/queue`: Pluggable multi-driver queue broker (Redis, NATS, RabbitMQ, PostgreSQL) from `fiber-extras`, used optionally at the API Gateway / Producer level for persistent buffering.

### Key Architectural Characteristics
1. **Minimal External Dependencies**: Implemented with pure-Go, minimal third-party footprint and without heavy framework overhead.
2. **Throttling Semaphore**:
   * Uses a channel-based semaphore (`chan struct{}`) bounded by `Concurrency` (default: 16).
   * Prevents system resource exhaustion (CPU/RAM) during sudden burst traffic.
3. **Panic Recovery**:
   * Every task is wrapped with deferred panic recovery.
   * If a tenant's transformation encounters an index-out-of-range or nil-pointer panic, the panic is intercepted, formatted with stack traces, and returned cleanly as an error without crashing the Supervisor daemon.
4. **Sub-Worker Fan-Out (`SubWorkerPool`)**:
   * A parent task can divide massive workloads into parallel sub-tasks:
     ```go
     func (t *ChunkedTask) Execute(ctx context.Context, pool supervisor.SubWorkerPool) error {
         for _, chunk := range t.chunks {
             pool.Go(func(ctx context.Context) error {
                 return processChunk(ctx, chunk)
             })
         }
         return nil
     }
     ```
   * If any sub-worker fails, the parent pool cancels sibling sub-workers and propagates the first error encountered.

---

## 2. Worker Lifecycle & Scale-to-Zero State Machine

Subzero workers follow a strict 5-stage lifecycle state machine designed to minimize idle cloud resource costs:

```text
[ DORMANT / ZERO ] ──(Incoming Task: Cold-Start)──► [ PROVISIONING ]
        ▲                                                    │
        │                                                    ▼
(Watchdog Timeout)                                    [ EXECUTING TASK ]
        │                                                    │
        └────────── [ STANDBY / WARM IDLE ] ◄──(Completed)───┘
```

### Lifecycle States

| State | Memory Footprint | Description |
| :--- | :--- | :--- |
| **1. Dormant** | **0 MB (0 RAM)** | The worker process is not running. Zero system resources are consumed. |
| **2. Provisioning** | Rising | The Supervisor detects an incoming task and cold-spawns a new worker process or Wasm instance. |
| **3. Executing Task** | Active Working Set | The worker is actively streaming and transforming document data. The idle watchdog is paused. |
| **4. Standby (Warm Idle)** | Minimal Resident | The task is finished. The worker enters warm standby. The internal `Watchdog` timer begins counting down. If a new task arrives before expiry, the worker immediately shifts back to *Executing Task* with zero cold-start delay. |
| **5. Termination** | Releasing to 0 | If the countdown reaches zero without new incoming work, the worker calls its termination procedure (`os.Exit(0)`), completely freeing memory back to the OS and returning to **Dormant**. |

---

## 3. Idle Watchdog Implementation

Located in [`internal/worker/watchdog.go`](../internal/worker/watchdog.go):

```go
type Watchdog struct {
    timeout   time.Duration
    onTimeout func()
    // internal synchronization...
}
```

* **`ActivityStarted()`**: Called when a frame arrives. Increments the active counter and stops the countdown timer.
* **`ActivityFinished()`**: Called when a frame execution completes. Decrements the active counter. If no active jobs remain, it starts a timer for `timeout` duration.
* **`onTimeout`**: Executed on expiry, triggering graceful worker closure and process exit.

---

## 4. Distributed Persistent Queue Integration (`v3/queue`)

To bridge asynchronous task ingestion from Main API Gateways to Subzero's Supervisor, Subzero integrates directly with [`github.com/rachmanzz/fiber-extras/v3/queue`](https://github.com/rachmanzz/fiber-extras/tree/main/v3/queue).

### Key Features
1. **Producer Task Enqueueing**:
   * API Gateways use `pkg/producer.Client.EnqueueTask()` or `EnqueueWithDriver()` to push `TaskMessage` payloads with tenant priority ordering (`queue.WithPriority(queue.PriorityHigh)`).
2. **Supervisor Queue Consumer**:
   * Configured via `supervisor.DispatcherConfig.QueueDriver` (supports MemoryDriver, Redis, NATS, PostgreSQL).
   * Automatically launches a background `queue.Consumer` inside `d.Start()` and gracefully terminates in `d.Stop()`.
3. **Queue Handler Registration**:
   * Handlers registered via `d.RegisterQueueHandler(topic, handler)` automatically decode incoming `protocol.TaskMessage` objects and execute them through the supervisor's `worker.Dispatcher` engine with full panic recovery and `SubWorkerPool` fan-out support.

