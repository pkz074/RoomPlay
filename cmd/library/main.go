// Music library entry point.
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/pkz074/RoomPlay/internal/library"
)

func main() {
	addr := envOrDefault("LIBRARY_ADDR", ":8090")
	publicURL := envOrDefault("LIBRARY_URL", "http://127.0.0.1:8090")
	catalogPath := envOrDefault("LIBRARY_CATALOG", "assets/music/catalog.json")
	audioRoot := envOrDefault("LIBRARY_AUDIO_ROOT", "assets/music")

	catalogFile, err := os.Open(catalogPath)
	if err != nil {
		log.Fatalf("open catalog: %v", err)
	}
	catalog, err := library.LoadCatalog(catalogFile, publicURL)
	catalogFile.Close()
	if err != nil {
		log.Fatalf("load catalog: %v", err)
	}

	libraryServer, err := library.NewServer(catalog, audioRoot)
	if err != nil {
		log.Fatalf("initialize library: %v", err)
	}
	defer libraryServer.Close()
	server := &http.Server{
		Addr:              addr,
		Handler:           libraryServer.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	fmt.Printf("RoomPlay library listening on %s\n", addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("serve library: %v", err)
	}
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
