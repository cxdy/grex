// grex-synth runs short-lived synthetic OpAMP agents against a grex server or
// gateway to measure connection scale. Each agent is a real opamp-go client
// that connects, heartbeats, optionally simulates a supervisor restart, and
// reports how long that took and what failed. See docs/developer/scaling.md.
//
// One process is bounded by ephemeral ports and memory: plan on roughly 50k
// agents per node and shard across nodes for larger fleets.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/dennisme/grex/internal/synth"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "grex-synth:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out, errOut io.Writer) error {
	cfg, err := parseConfig(args, errOut)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewTextHandler(errOut, &slog.HandlerOptions{Level: slog.LevelInfo}))
	report, err := synth.Run(ctx, cfg, logger)
	if err != nil {
		return err
	}
	return printReport(out, report)
}

func parseConfig(args []string, errOut io.Writer) (synth.Config, error) {
	fs := flag.NewFlagSet("grex-synth", flag.ContinueOnError)
	fs.SetOutput(errOut)
	var cfg synth.Config
	fs.StringVar(&cfg.ServerURL, "url", "", "OpAMP server URL (ws://, wss://, http://, https://)")
	fs.IntVar(&cfg.Agents, "agents", 1, "number of synthetic agents to run")
	fs.IntVar(&cfg.RampPerSec, "ramp", 0, "new agents started per second (0 = all at once)")
	fs.DurationVar(&cfg.Heartbeat, "heartbeat", 30*time.Second, "OpAMP heartbeat interval")
	fs.DurationVar(&cfg.Duration, "duration", time.Minute, "how long the run lasts")
	fs.DurationVar(&cfg.RestartAfter, "restart-after", 0, "simulate a supervisor restart this far into the run (0 = none)")
	fs.StringVar(&cfg.ServiceName, "service-name", "otelcol-contrib", "agent service.name attribute")
	fs.StringVar(&cfg.CertFile, "cert", "", "client certificate file (mTLS)")
	fs.StringVar(&cfg.KeyFile, "key", "", "client private key file (mTLS)")
	fs.StringVar(&cfg.CAFile, "ca", "", "CA certificate file for server verification")
	if err := fs.Parse(args); err != nil {
		return synth.Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return synth.Config{}, err
	}
	return cfg, nil
}

func printReport(out io.Writer, r synth.Report) error {
	var b strings.Builder
	fmt.Fprintln(&b, "── synth report ──")
	fmt.Fprintf(&b, "agents:     %d\n", r.Total)
	fmt.Fprintf(&b, "connected:  %d\n", r.Connected)
	fmt.Fprintf(&b, "failed:     %d\n", r.Failed)
	fmt.Fprintf(&b, "restarted:  %d\n", r.Restarted)
	fmt.Fprintf(&b, "connect p50/p99/max: %s / %s / %s\n", r.ConnectP50, r.ConnectP99, r.ConnectMax)
	if len(r.Errors) > 0 {
		fmt.Fprintln(&b, "errors:")
		for msg, n := range r.Errors {
			fmt.Fprintf(&b, "  %5d  %s\n", n, msg)
		}
	}
	_, err := io.WriteString(out, b.String())
	return err
}
