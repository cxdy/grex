# Tracing

## Current state

**grex does not export distributed traces of its own execution.**

There is:

- No OpenTelemetry SDK instrumentation in the grex process for spans
- No OTLP exporter for grex self-telemetry
- No trace context propagation on the JSON API or OpAMP path from grex’s side

The only related product surface is **agent-reported own-telemetry metadata**
over OpAMP (agents describing where *they* send telemetry). That does not
create grex server spans.

## Why this page exists

Operators often expect metrics + logs + traces as a triad. For grex 1.0 the
honest answer is:

| Signal | Status |
|--------|--------|
| Metrics | Yes — Prometheus |
| Logs | Yes — stderr slog |
| Traces | **No** grex server traces |

Design SPEC: Prometheus scrape is the only export path for grex’s own
telemetry in 1.0.

## What you can do instead

- Use **metrics** for SLIs (API latency histograms, OpAMP error counters,
  fleet connected gauges)
- Use **logs** for sparse diagnostic context
- Use **pprof** ([debug pprof](debug-pprof.md)) for in-process CPU/heap
  analysis when metrics show a problem
- Trace **collectors** and apps in the fleet with your existing OTel
  pipelines; grex observes OpAMP health, not application spans

## Future

Server-side tracing may appear in a later release; nothing in the current
code implements it. When it does, expect this page to document exporter
config, sampler defaults, and which spans wrap OpAMP vs HTTP API paths.
Until then, do not configure collectors to scrape OTLP from grex for grex’s
own traces—there is nothing listening for that purpose.
