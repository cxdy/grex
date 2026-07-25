# Debug pprof

## Enable

```yaml
debug:
  pprof_enabled: true
```

Or:

```sh
export GREX_DEBUG_PPROF_ENABLED=true
```

When enabled, grex logs a **warning** at startup and mounts standard
`net/http/pprof` handlers on the **telemetry** listener under
`/debug/pprof/`.

## Endpoints

Including (via pprof index dispatch):

- `/debug/pprof/` — index
- `/debug/pprof/cmdline`
- `/debug/pprof/profile` — CPU
- `/debug/pprof/symbol`
- `/debug/pprof/trace`
- Runtime profiles: heap, goroutine, block, mutex, threadcreate, …

Handlers are registered on grex’s **own** mux, never on
`http.DefaultServeMux`.

## Security

pprof can expose **memory contents** and CPU profiling adds load. Only
enable when:

- The telemetry listener is not reachable from untrusted networks
- You are actively debugging a performance issue

Disable again after the investigation.

## Usage example

```sh
go tool pprof http://127.0.0.1:9090/debug/pprof/heap
go tool pprof http://127.0.0.1:9090/debug/pprof/profile?seconds=30
```

## Default

`pprof_enabled` defaults to **false**.
