// Room player entry point.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/pkz074/RoomPlay/internal/player"
	"github.com/pkz074/RoomPlay/internal/protocol"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	roomID := strings.TrimSpace(os.Getenv("ROOM_ID"))
	if !protocol.ValidRoomID(roomID) {
		return fmt.Errorf("ROOM_ID must be configured using lowercase letters, digits, hyphens, or underscores")
	}
	roomName := envOrDefault("ROOM_NAME", roomID)
	natsURL := envOrDefault("NATS_URL", "nats://127.0.0.1:4222")
	libraryURL := envOrDefault("LIBRARY_URL", "http://127.0.0.1:8090")
	mode := strings.ToLower(strings.TrimSpace(envOrDefault("PLAYER_MODE", "simulation")))
	if mode != "simulation" {
		return fmt.Errorf("unsupported PLAYER_MODE %q; only simulation is currently available", mode)
	}

	loadCtx, cancelLoad := context.WithTimeout(context.Background(), 6*time.Second)
	tracks, err := player.FetchTrackIndex(loadCtx, libraryURL, &http.Client{Timeout: 5 * time.Second})
	cancelLoad()
	if err != nil {
		return fmt.Errorf("load player track catalog: %w", err)
	}
	processor := player.NewCommandProcessor(tracks, player.NewSimulationBackend())
	defer processor.Close()
	instanceID, err := newInstanceID()
	if err != nil {
		return fmt.Errorf("create player instance ID: %w", err)
	}

	reconnectEpoch := &atomic.Uint64{}
	connection, err := nats.Connect(
		natsURL,
		nats.Name("RoomPlay player "+roomID),
		nats.Timeout(3*time.Second),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(time.Second),
		nats.RetryOnFailedConnect(true),
		nats.DrainTimeout(3*time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			reconnectEpoch.Add(1)
			log.Printf("NATS disconnected: %v", err)
		}),
		nats.ReconnectHandler(func(_ *nats.Conn) {
			reconnectEpoch.Add(1)
			log.Printf("NATS reconnected; player will renew registration")
		}),
	)
	if err != nil {
		return fmt.Errorf("connect to NATS: %w", err)
	}
	defer connection.Drain()

	playerClient, err := player.NewNATSPlayer(connection, processor, player.NATSPlayerConfig{
		RoomID:         roomID,
		Name:           roomName,
		InstanceID:     instanceID,
		ReconnectEpoch: reconnectEpoch,
	})
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	log.Printf("RoomPlay player starting: room=%s instance=%s mode=%s", roomID, instanceID, mode)
	if err := playerClient.Run(ctx); err != nil {
		return fmt.Errorf("run player: %w", err)
	}
	return nil
}

func newInstanceID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
