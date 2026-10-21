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
	"strconv"
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

type config struct {
	apiKey     string
	vin        string
	interval   time.Duration
	include    string
	baseURL    string
	listenAddr string
	staleAfter time.Duration
	logLevel   string
}

func main() {
	cfg := loadConfig()

	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.logLevel)); err != nil {
		fatal("invalid log level", err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	key := strings.TrimSpace(cfg.apiKey)
	if key == "" {
		fatal("configuration", errors.New("no API key: set SKODA_API_KEY"))
	}
	cfg.vin = strings.TrimSpace(cfg.vin)
	if cfg.vin == "" {
		fatal("configuration", errors.New("no VIN: set SKODA_VIN"))
	}
	if cfg.interval < poller.MinInterval {
		logger.Warn("poll interval raised to stay under the API rate limit", "requested", cfg.interval, "used", poller.MinInterval)
		cfg.interval = poller.MinInterval
	}

	store := poller.NewStore(cfg.vin)
	requests := poller.NewRequestsCounter()

	reg := prometheus.NewRegistry()
	reg.MustRegister(
		&collector.Collector{Store: store, StaleAfter: cfg.staleAfter},
		requests,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	p := &poller.Poller{
		Fetcher:  api.New(cfg.baseURL, key, nil),
		Store:    store,
		VIN:      cfg.vin,
		Include:  splitList(cfg.include),
		Interval: cfg.interval,
		Logger:   logger,
		Requests: requests,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><h1>Skoda exporter</h1><p><a href="/metrics">Metrics</a></p></body></html>`))
	})
	srv := &http.Server{Addr: cfg.listenAddr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	// Bind before polling so a port clash does not waste API quota.
	ln, err := net.Listen("tcp", cfg.listenAddr)
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
			logger.Error("shutdown", "err", err)
		}
	}()

	logger.Info("starting", "listen", cfg.listenAddr, "vin", cfg.vin, "interval", cfg.interval, "base_url", cfg.baseURL)
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fatal("http server", err)
	}
	// Serve returns as soon as Shutdown starts; wait for in-flight requests to drain.
	<-done
}

func loadConfig() config {
	return config{
		apiKey:     env("SKODA_API_KEY", ""),
		vin:        env("SKODA_VIN", ""),
		interval:   envDuration("SKODA_POLL_INTERVAL", 5*time.Minute),
		include:    env("SKODA_INCLUDE", ""),
		baseURL:    env("SKODA_BASE_URL", api.ProductionURL),
		listenAddr: env("LISTEN_ADDR", ":9877"),
		staleAfter: envDuration("SKODA_STALE_AFTER", time.Hour),
		logLevel:   env("LOG_LEVEL", "info"),
	}
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
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
	// Bare numbers are seconds.
	if n, err := strconv.Atoi(v); err == nil {
		return time.Duration(n) * time.Second
	}
	fatal("configuration", fmt.Errorf("%s: invalid duration %q", name, v))
	return 0
}

func fatal(msg string, err error) {
	slog.Error(msg, "err", err)
	os.Exit(1)
}
