// Command skoda-exporter exports MyŠkoda vehicle data as Prometheus metrics.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
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
	apiKeyFile string
	vins       string
	interval   time.Duration
	include    string
	baseURL    string
	listenAddr string
	staleAfter time.Duration
	logLevel   string
}

func main() {
	cfg := parseFlags(os.Args[1:])

	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.logLevel)); err != nil {
		fatal("invalid log level", err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	key, err := loadKey(cfg)
	if err != nil {
		fatal("loading API key", err)
	}
	vins := splitList(cfg.vins)
	if len(vins) == 0 {
		fatal("configuration", errors.New("no VINs: set SKODA_VINS or -vins"))
	}
	if cfg.interval < poller.MinInterval {
		logger.Warn("poll interval raised to stay under the API rate limit", "requested", cfg.interval, "used", poller.MinInterval)
		cfg.interval = poller.MinInterval
	}

	store := poller.NewStore(vins)
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
		VINs:     vins,
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
		w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><body><h1>Skoda exporter</h1><p><a href="/metrics">Metrics</a></p></body></html>`))
	})
	srv := &http.Server{Addr: cfg.listenAddr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	go p.Run(ctx)
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()

	logger.Info("starting", "listen", cfg.listenAddr, "vins", len(vins), "interval", cfg.interval, "base_url", cfg.baseURL)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fatal("http server", err)
	}
}

func parseFlags(args []string) config {
	var cfg config
	fs := flag.NewFlagSet("skoda-exporter", flag.ExitOnError)
	fs.StringVar(&cfg.apiKey, "api-key", env("SKODA_API_KEY", ""), "MyŠkoda API key (env SKODA_API_KEY); prefer -api-key-file")
	fs.StringVar(&cfg.apiKeyFile, "api-key-file", env("SKODA_API_KEY_FILE", ""), "file holding the API key (env SKODA_API_KEY_FILE)")
	fs.StringVar(&cfg.vins, "vins", env("SKODA_VINS", ""), "comma-separated VINs to poll (env SKODA_VINS)")
	fs.DurationVar(&cfg.interval, "poll-interval", envDuration("SKODA_POLL_INTERVAL", 5*time.Minute), "time between calls per VIN, at least 4m (env SKODA_POLL_INTERVAL)")
	fs.StringVar(&cfg.include, "include", env("SKODA_INCLUDE", ""), "comma-separated response parts; empty means all (env SKODA_INCLUDE)")
	fs.StringVar(&cfg.baseURL, "base-url", env("SKODA_BASE_URL", api.ProductionURL), "API base URL; "+api.SandboxURL+" for the sandbox (env SKODA_BASE_URL)")
	fs.StringVar(&cfg.listenAddr, "listen-address", env("LISTEN_ADDR", ":9877"), "address for /metrics (env LISTEN_ADDR)")
	fs.DurationVar(&cfg.staleAfter, "stale-after", envDuration("SKODA_STALE_AFTER", time.Hour), "drop vehicle metrics when the last success is older (env SKODA_STALE_AFTER)")
	fs.StringVar(&cfg.logLevel, "log-level", env("LOG_LEVEL", "info"), "debug, info, warn or error (env LOG_LEVEL)")
	fs.Parse(args)
	return cfg
}

func loadKey(cfg config) (string, error) {
	if cfg.apiKeyFile != "" {
		b, err := os.ReadFile(cfg.apiKeyFile)
		if err != nil {
			return "", err
		}
		cfg.apiKey = string(b)
	}
	key := strings.TrimSpace(cfg.apiKey)
	if key == "" {
		return "", errors.New("no API key: set SKODA_API_KEY_FILE or SKODA_API_KEY")
	}
	return key, nil
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
