package library

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"unicode"

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
	base, err := url.Parse(strings.TrimSpace(publicURL))
	if err != nil || base == nil || (base.Scheme != "http" && base.Scheme != "https") || base.Hostname() == "" ||
		base.User != nil || base.Opaque != "" || base.RawQuery != "" || base.ForceQuery || base.Fragment != "" ||
		strings.Contains(publicURL, "#") || (base.Path != "" && base.Path != "/") || base.RawPath != "" {
		return nil, fmt.Errorf("public library URL must be an absolute HTTP(S) origin without credentials, query, fragment, or path prefix")
	}
	if port := base.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("public library URL has an invalid port")
		}
	} else if strings.HasSuffix(base.Host, ":") {
		return nil, fmt.Errorf("public library URL has an empty port")
	}
	var file catalogFile
	// Bound startup input and require exactly one JSON object with a tracks array.
	limited := &io.LimitedReader{R: r, N: (1 << 20) + 1}
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return nil, fmt.Errorf("decode catalog: %w", err)
	}

	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("catalog must contain exactly one JSON object")
	}
	if limited.N == 0 {
		return nil, fmt.Errorf("catalog exceeds 1 MiB")
	}
	if file.Tracks == nil {
		return nil, fmt.Errorf("catalog tracks must be an array (use [] for an empty catalog)")
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
		audioURL := *base
		audioURL.Path = "/audio/" + entry.AudioFile
		catalog.tracks = append(catalog.tracks, protocol.Track{
			ID:         entry.ID,
			Title:      entry.Title,
			Artist:     entry.Artist,
			DurationMs: entry.DurationMs,
			URL:        audioURL.String(),
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
	if strings.ContainsAny(name, `/\\%:*?"<>|`) || strings.Contains(name, "..") || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return false
	}
	// Reject Windows device aliases on every OS, so a catalog is portable.
	stem := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	if stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" ||
		(len([]rune(stem)) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && strings.ContainsRune("123456789¹²³", []rune(stem)[3])) {
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
