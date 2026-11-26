// Coordinator serves the browser API, SSE and static web controller.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/pkz074/RoomPlay/internal/coordinator"
)

func main() {
	logger := log.New(os.Stderr, "coordinator: ", log.LstdFlags)
	if err := run(logger); err != nil {
		logger.Fatalf("fatal: %v", err)
	}
}

func run(logger *log.Logger) error {
	natsURL := envOrDefault("NATS_URL", "nats://127.0.0.1:4222")
	libraryURL := envOrDefault("LIBRARY_URL", "http://127.0.0.1:8090")
	address := envOrDefault("COORDINATOR_ADDR", ":8080")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	nc, err := nats.Connect(natsURL,
		nats.Name("roomplay-coordinator"),
		nats.Timeout(5*time.Second),
		nats.MaxReconnects(-1),
		nats.ReconnectBufSize(-1), // never replay browser commands after reconnect
		nats.ReconnectWait(time.Second),
	)
	if err != nil {
		return err
	}
	defer nc.Close()

	reg := coordinator.NewRegistry(coordinator.SystemClock{}, func(c coordinator.Change) {
		logger.Printf("availability: room=%s -> %s", c.RoomID, c.Availability)
	})
	svc := coordinator.NewService(nc, reg, logger)
	if err := svc.Start(); err != nil {
		nc.Close()
		return err
	}
	go svc.RunExpiry(ctx, time.Second)
	handler, err := coordinator.NewHTTPHandler(svc, coordinator.HTTPOptions{LibraryURL: libraryURL, WebFS: os.DirFS(envOrDefault("WEB_ROOT", "web"))})
	if err != nil {
		_ = svc.Stop()
		return err
	}
	server := &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	serverError := make(chan error, 1)
	go func() { serverError <- server.ListenAndServe() }()

	logger.Printf("running; HTTP=%s NATS_URL=%s", address, natsURL)
	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-serverError:
	}
	stop()
	logger.Printf("shutting down")
	_ = svc.Stop()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil {
		_ = server.Close()
		return err
	}
	if errors.Is(serveErr, http.ErrServerClosed) {
		return nil
	}
	return serveErr
}

func envOrDefault(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
