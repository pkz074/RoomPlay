// Coordinator entry point. This PR wires NATS registration/heartbeat only;
// REST and SSE come in separate PRs.
package main

import (
	"context"
	"log"
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	nc, err := nats.Connect(natsURL,
		nats.Name("roomplay-coordinator"),
		nats.Timeout(5*time.Second),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(time.Second),
	)
	if err != nil {
		return err
	}

	reg := coordinator.NewRegistry(coordinator.SystemClock{}, func(c coordinator.Change) {
		logger.Printf("availability: room=%s -> %s", c.RoomID, c.Availability)
	})
	svc := coordinator.NewService(nc, reg, logger)
	if err := svc.Start(); err != nil {
		nc.Close()
		return err
	}
	go svc.RunExpiry(ctx, time.Second)

	logger.Printf("running; NATS_URL=%s", natsURL)
	<-ctx.Done()
	logger.Printf("shutting down")
	return svc.Stop()
}

func envOrDefault(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
