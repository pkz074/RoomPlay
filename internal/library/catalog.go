package library

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/pkz074/RoomPlay/internal/protocol"
)

// CatalogEntry includes library-only fields that must not leak into the public Track response.
type CatalogEntry struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Artist      string `json:"artist"`
	DurationMs  int64  `json:"duration_ms"`
	AudioFile   string `json:"audio_file"`
	Source      string `json:"source"`
	License     string `json:"license"`
	LicenseURL  string `json:"license_url"`
	Attribution string `json:"attribution"`
}

type catalogFile struct {
	Tracks []CatalogEntry `json:"tracks"`
}

// Catalog is an immutable, validated allowlist of catalog tracks and audio filenames.
type Catalog struct {
	tracks     []protocol.Track
	byID       map[string]CatalogEntry
	byFilename map[string]CatalogEntry
}

func LoadCatalog(r io.Reader, publicURL string) (*Catalog, error) {
	var file catalogFile
	decoder := json.NewDecoder(r)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return nil, fmt.Errorf("decode catalog: %w", err)
	}

	publicURL = strings.TrimRight(strings.TrimSpace(publicURL), "/")
	if publicURL == "" {
		return nil, fmt.Errorf("public library URL is required")
	}

	catalog := &Catalog{
		tracks:     make([]protocol.Track, 0, len(file.Tracks)),
		byID:       make(map[string]CatalogEntry, len(file.Tracks)),
		byFilename: make(map[string]CatalogEntry, len(file.Tracks)),
	}

	for _, entry := range file.Tracks {
		if err := validateEntry(entry); err != nil {
			return nil, err
		}
		if _, exists := catalog.byID[entry.ID]; exists {
			return nil, fmt.Errorf("duplicate track id %q", entry.ID)
		}
		if _, exists := catalog.byFilename[entry.AudioFile]; exists {
			return nil, fmt.Errorf("duplicate audio file %q", entry.AudioFile)
		}

		catalog.byID[entry.ID] = entry
		catalog.byFilename[entry.AudioFile] = entry
		catalog.tracks = append(catalog.tracks, protocol.Track{
			ID:         entry.ID,
			Title:      entry.Title,
			Artist:     entry.Artist,
			DurationMs: entry.DurationMs,
			URL:        publicURL + "/audio/" + entry.AudioFile,
		})
	}

	return catalog, nil
}

func validateEntry(entry CatalogEntry) error {
	if strings.TrimSpace(entry.ID) == "" {
		return fmt.Errorf("track id is required")
	}
	if strings.TrimSpace(entry.Title) == "" {
		return fmt.Errorf("track %q: title is required", entry.ID)
	}
	if strings.TrimSpace(entry.Artist) == "" {
		return fmt.Errorf("track %q: artist is required", entry.ID)
	}
	if entry.DurationMs < 0 {
		return fmt.Errorf("track %q: duration_ms must be nonnegative", entry.ID)
	}
	if !safeAudioFilename(entry.AudioFile) {
		return fmt.Errorf("track %q: unsafe audio filename", entry.ID)
	}
	if strings.TrimSpace(entry.Source) == "" {
		return fmt.Errorf("track %q: source is required", entry.ID)
	}
	if strings.TrimSpace(entry.License) == "" {
		return fmt.Errorf("track %q: license is required", entry.ID)
	}
	if strings.TrimSpace(entry.LicenseURL) == "" {
		return fmt.Errorf("track %q: license_url is required", entry.ID)
	}
	if strings.TrimSpace(entry.Attribution) == "" {
		return fmt.Errorf("track %q: attribution is required", entry.ID)
	}
	return nil
}

func safeAudioFilename(name string) bool {
	if name == "" || name != strings.TrimSpace(name) {
		return false
	}
	if strings.ContainsAny(name, `/\\`) || strings.Contains(name, "..") {
		return false
	}
	return strings.HasSuffix(strings.ToLower(name), ".mp3")
}

func (c *Catalog) Tracks() []protocol.Track {
	tracks := make([]protocol.Track, len(c.tracks))
	copy(tracks, c.tracks)
	return tracks
}

func (c *Catalog) EntryByFilename(name string) (CatalogEntry, bool) {
	entry, exists := c.byFilename[name]
	return entry, exists
}
