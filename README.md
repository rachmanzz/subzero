# Subzero

[![Go Version](https://img.shields.io/badge/Go-1.26+-00ADD8?style=flat&logo=go)](https://golang.org)
[![License: AGPL-3.0](https://img.shields.io/badge/License-AGPL_3.0-blue.svg)](LICENSE)

**Subzero** is a distributed, sandboxed runtime engine built on a *scale-to-zero* computing architecture. It acts as an on-demand workload execution bridge for tenant data parsing and transformations (e.g., massive spreadsheet processing), minimizing idle resources while ensuring strict memory isolation and blast-radius containment.

---

## ❄️ Key Architecture Principles

1. **Zero Idle Cost (Scale-to-Zero)**: Workers automatically terminate and release 100% of host memory when the idle watchdog window expires.
2. **Sub-100ms Cold Starts**: Native OS process and in-memory IPC options allow lightning-fast spin-up times.
3. **Zero Payload in Control Plane**: Large files never traverse the command network; clients upload directly to object storage via pre-signed URLs, and workers stream directly from storage.
4. **Decoupled Transports**: Pluggable transport adapters:
   - `unix`: Fast local Unix Domain Sockets.
   - `tcp`: Host-to-host cross-node stream with optional mTLS.
   - `http`: HTTP Upgrade full-duplex stream for ingress/proxy compatibility.
   - `inproc`: In-memory `net.Pipe` for zero-overhead unit testing and embedded runtimes.
5. **Robust Concurrency & Throttling**: Supervisor uses an internal task dispatcher with throttling semaphores, panic recovery, and sub-worker fan-out.

---

## 📁 Repository Structure

```text
subzero/
├── cmd/
│   ├── subzero-supervisor/   # Daemon coordinating worker lifecycles & task queueing
│   ├── subzero-worker/       # Standalone scale-to-zero worker executable
│   └── subzero-cli/          # Multi-command CLI utility (module lifecycle, upload requests, diagnostics, and tasks)
├── pkg/
│   ├── producer/             # Client SDK for API Gateways (submit tasks & upload slots)
│   ├── protocol/             # Wire framing (HMAC-SHA256, 4-byte length prefix) & messages
│   └── transport/            # Transports: unix (IPC), tcp (mTLS), http, inproc
├── internal/
│   ├── supervisor/           # Dispatcher engine, throttling semaphore & panic safety
│   ├── worker/               # Worker coordinator, idle watchdog & auto-termination
│   └── storage/              # Storage abstraction (Local, S3, MinIO)
└── Makefile                  # Build, test, and cleanup scripts
```

---

## 🚀 Getting Started

### 1. Build Binaries
```bash
make build
```

The resulting binaries will be placed in `./bin/`:
- `bin/subzero-supervisor`
- `bin/subzero-worker`
- `bin/subzero-cli`

### 2. Run Tests
```bash
make test
```

### 3. Quick Run Demo (Single Node IPC)

In terminal 1, start the worker with a 15-second idle timeout:
```bash
./bin/subzero-worker -scheme unix -endpoint /tmp/subzero-worker.sock -idle-timeout 15s
```

In terminal 2, execute a task using the CLI:
```bash
./bin/subzero-cli -scheme unix -endpoint /tmp/subzero-worker.sock -tenant acme-corp
```

Notice the sub-millisecond response time and observe the worker auto-terminating (scale-to-zero) once 15 seconds of inactivity elapses.

---

## 📄 License

This project is **dual licensed**. The default license currently in effect for all code in this repository is the **GNU Affero General Public License v3.0 (AGPL-3.0)**.

- **Default License**: [GNU AGPL-3.0](LICENSE)
- **Copyright & Reserved Rights**: [NOTICE](NOTICE)
- **Dual Licensing Policy & Commercial Option (SSPL-1.0)**: [LICENSING.md](LICENSING.md)
