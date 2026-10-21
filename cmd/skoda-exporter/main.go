// Command skoda-exporter exports MyŠkoda vehicle data as Prometheus metrics.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"skoda-exporter/internal/api"
	"skoda-exporter/internal/collector"
	"skoda-exporter/internal/poller"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	apiKey := strings.TrimSpace(os.Getenv("SKODA_API_KEY"))
	vin := strings.TrimSpace(os.Getenv("SKODA_VIN"))
	interval := envDuration("SKODA_POLL_INTERVAL", 5*time.Minute)
	include := strings.TrimSpace(os.Getenv("SKODA_INCLUDE"))
	baseURL := env("SKODA_BASE_URL", api.ProductionURL)
	listenAddr := env("LISTEN_ADDR", ":9877")
	staleAfter := envDuration("SKODA_STALE_AFTER", time.Hour)
	logLevel := env("LOG_LEVEL", "info")

	var level slog.Level
	if err := level.UnmarshalText([]byte(logLevel)); err != nil {
		fatal("invalid log level", err)
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	if apiKey == "" {
		fatal("configuration", errors.New("no API key: set SKODA_API_KEY"))
	}
	if vin == "" {
		fatal("configuration", errors.New("no VIN: set SKODA_VIN"))
	}
	if interval < poller.MinInterval {
		slog.Warn("poll interval raised to stay under the API rate limit", "requested", interval, "used", poller.MinInterval)
		interval = poller.MinInterval
	}

	store := poller.NewStore(vin)
	requests := poller.NewRequestsCounter()

	reg := prometheus.NewRegistry()
	reg.MustRegister(
		&collector.Collector{Store: store, StaleAfter: staleAfter},
		requests,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	p := &poller.Poller{
		Fetcher:  api.New(baseURL, apiKey),
		Store:    store,
		VIN:      vin,
		Include:  include,
		Interval: interval,
		Requests: requests,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	srv := &http.Server{Addr: listenAddr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	// Bind before polling so a port clash does not waste API quota.
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		fatal("listen", err)
	}

	go p.Run(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		stop() // restore default signal handling so a second signal kills the process during the drain
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdown); err != nil {
			slog.Error("shutdown", "err", err)
		}
	}()

	slog.Info("starting", "listen", listenAddr, "vin", vin, "interval", interval, "base_url", baseURL)
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fatal("http server", err)
	}
	// Serve returns as soon as Shutdown starts; wait for in-flight requests to drain.
	<-done
}

func env(name, def string) string {
	if v, ok := os.LookupEnv(name); ok {
		return v
	}
	return def
}

func envDuration(name string, def time.Duration) time.Duration {
	v, ok := os.LookupEnv(name)
	if !ok {
		return def
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	fatal("configuration", fmt.Errorf("%s: invalid duration %q", name, v))
	return 0
}

func fatal(msg string, err error) {
	slog.Error(msg, "err", err)
	os.Exit(1)
}
