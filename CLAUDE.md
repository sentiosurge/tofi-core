# Tofi Core - Project Status & Roadmap

## 🚀 Current State (Production-Grade Prototype)
Tofi is a lightweight, distributed workflow engine designed for multi-tenancy and high reliability.

### Core Features
- **Dual-Mode Binary**: Support for immediate CLI execution (`run`) and HTTP Daemon mode (`server`).
- **Strong Contract Logic**: Top-level `data` and `secrets` blocks define strict input interfaces for workflows.
- **Implicit Resolution**: Smart detection of `data.key`, `secrets.key`, and `node_id.field` in configurations.
- **Structured Handoff**: `uses: user/repo` style workflow-in-workflow calls with explicit `data`/`secrets` mapping.
- **Production Persistence**: SQLite-backed execution history and real-time state snapshots (no more orphan JSON files).
- **JWT Auth**: Full JWT authentication with multi-tenancy awareness. Every task is tied to a `User`.
- **Isolated Storage**: Strict path namespacing: `.tofi/{user}/{logs|uploads|artifacts}/{exec_id}/`.
- **Magic Environment**: Automatic injection of `TOFI_UPLOADS_DIR` and `TOFI_ARTIFACTS_DIR` into shell nodes.

---

## 🛠 Next Steps (Stability & Hardening)

### ✅ 1. Zombie Recovery (Self-Healing) - COMPLETED
- **Solution**: Implemented boot-time scanner that finds `RUNNING` tasks and resumes them via worker pool.
- **Implementation**: [internal/engine/recovery.go](internal/engine/recovery.go), [internal/server/recovery.go](internal/server/recovery.go)
- **Test**: [scripts/test_zombie_recovery.sh](scripts/test_zombie_recovery.sh)

### ✅ 2. Worker Pool (Traffic Throttling) - COMPLETED
- **Solution**: Implemented semaphore-based worker pool with configurable concurrency limit.
- **Implementation**: [internal/server/workerpool.go](internal/server/workerpool.go)
- **Features**:
  - Configurable `MaxConcurrentWorkflows` (default: 10)
  - Queue buffering up to 100 tasks
  - Graceful shutdown
  - Real-time statistics API (`GET /api/v1/stats`)
- **Test**: [scripts/test_workerpool.sh](scripts/test_workerpool.sh)
- **Usage**: `./tofi server -workers 5` (设置最大并发数为 5)

### ✅ 3. Hard Timeouts - COMPLETED
- **Solution**: Implemented context-based timeout control at both node and workflow levels.
- **Implementation**:
  - Node-level: `timeout` field in Node struct, enforced in [internal/engine/engine.go](internal/engine/engine.go)
  - Global workflow: `timeout` field in Workflow struct with `context.WithTimeout`
- **Features**:
  - Node timeout: Individual task time limit with automatic cancellation
  - Global timeout: Entire workflow time limit
  - Graceful error propagation and on_failure support
  - TIMEOUT status in logs and stats
- **Test**: [workflows/timeout_node_test.yaml](workflows/timeout_node_test.yaml), [workflows/timeout_global_test.yaml](workflows/timeout_global_test.yaml)
- **Docs**: [docs/TIMEOUT_GUIDE.md](docs/TIMEOUT_GUIDE.md)

### ✅ 4. Component Versioning - COMPLETED
- **Solution**: Implemented file-based component versioning with `@version` syntax support.
- **Implementation**:
  - Modified [internal/parser/parser.go](internal/parser/parser.go) to parse `@version` syntax
  - File naming: `component.yaml` (default), `component.v1.yaml`, `component.v2.yaml`
- **Features**:
  - `uses: tofi/component` - Default version
  - `uses: tofi/component@v1` - Specific version
  - Backward compatible - No version = default version
  - Clear error messages for missing versions
- **Test Components**:
  - [internal/toolbox/greet.yaml](internal/toolbox/greet.yaml)
  - [internal/toolbox/greet.v1.yaml](internal/toolbox/greet.v1.yaml)
  - [internal/toolbox/greet.v2.yaml](internal/toolbox/greet.v2.yaml)
  - [internal/toolbox/greet.v3.yaml](internal/toolbox/greet.v3.yaml)
- **Test**: [workflows/version_test.yaml](workflows/version_test.yaml)
- **Docs**: [docs/VERSIONING_GUIDE.md](docs/VERSIONING_GUIDE.md)

### 5. Compute Sandboxing
- **Current**: DirectExecutor with 3-layer software isolation (command blocklist + safe PATH + macOS seatbelt)
- **Future**: Linux namespaces + chroot for production multi-tenant isolation (no Docker dependency)

---

## 📖 Useful Commands
- `./tofi init`: Initialize workspace, configure AI provider and auth.
- `./tofi start`: Start the engine as a background daemon (default port 8321).
- `./tofi start -p 9000 -w 5`: Start on port 9000 with 5 max concurrent workers.
- `./tofi stop`: Stop the engine.
- `./tofi`: Launch the interactive TUI.
- `curl http://localhost:8321/api/v1/stats`: Check worker pool statistics.
- `curl http://localhost:8321/docs`: API documentation.
