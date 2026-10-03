package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ANISHG-26/ottawa-fleet-app/services/internal/scenario"
)

func main() {
	var c scenario.Config
	flag.Int64Var(&c.Seed, "seed", 1, "non-negative deterministic input seed")
	flag.IntVar(&c.Count, "count", 10, "number of ride requests (1..100)")
	flag.DurationVar(&c.Interval, "interval", time.Second, "time between requests (100ms..10s); total submission duration <=60s")
	flag.DurationVar(&c.Observe, "observe", 60*time.Second, "maximum outcome observation time (0..60s)")
	flag.StringVar(&c.Mode, "mode", "surge", "scenario mode: surge, normal, or fault")
	flag.StringVar(&c.FleetURL, "fleet-url", "http://localhost:8080", "local Fleet API URL (loopback port 8080 only)")
	flag.StringVar(&c.RideURL, "ride-url", "http://localhost:8081", "local Ride API URL (loopback port 8081 only)")
	flag.IntVar(&c.FaultLatencyMS, "fault-latency-ms", 0, "opt-in Fleet latency fault, 0..1000 (fault mode only)")
	flag.IntVar(&c.FaultErrorPercent, "fault-error-percent", 0, "opt-in Fleet 500 fault rate, 0..50 (fault mode only)")
	flag.Parse()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	report, err := scenario.Run(ctx, c)
	encoded, _ := json.MarshalIndent(report, "", "  ")
	fmt.Println(string(encoded))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
