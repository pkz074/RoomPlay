package player

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/pkz074/RoomPlay/internal/protocol"
)

const maxTrackCatalogBytes = 2 << 20

// TrackIndex is an immutable catalog lookup keyed by the public track ID.
type TrackIndex map[string]protocol.Track

// FetchTrackIndex loads and validates the library catalog over HTTP.
func FetchTrackIndex(ctx context.Context, libraryURL string, client *http.Client) (TrackIndex, error) {
	base, err := url.Parse(strings.TrimSpace(libraryURL))
	if err != nil || base == nil || (base.Scheme != "http" && base.Scheme != "https") || base.Hostname() == "" ||
		base.User != nil || base.Opaque != "" || base.RawQuery != "" || base.ForceQuery || base.Fragment != "" ||
		(base.Path != "" && base.Path != "/") || base.RawPath != "" {
		return nil, fmt.Errorf("library URL must be an HTTP(S) origin")
	}
	base.Path = "/tracks"
	base.RawPath = ""
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create catalog request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("request library catalog: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("library catalog returned HTTP %d", response.StatusCode)
	}

	limited := &io.LimitedReader{R: response.Body, N: maxTrackCatalogBytes + 1}
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	var result protocol.TracksResponse
	if err := decoder.Decode(&result); err != nil {
		return nil, fmt.Errorf("decode library catalog: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("library catalog must contain exactly one JSON object")
	}
	if limited.N == 0 {
		return nil, fmt.Errorf("library catalog exceeds %d bytes", maxTrackCatalogBytes)
	}
	if result.Tracks == nil {
		return nil, fmt.Errorf("library catalog tracks must be an array")
	}
	return NewTrackIndex(result.Tracks)
}

// NewTrackIndex validates and indexes public track metadata.
func NewTrackIndex(tracks []protocol.Track) (TrackIndex, error) {
	index := make(TrackIndex, len(tracks))
	for _, track := range tracks {
		if !protocol.ValidIdentifier(track.ID) || strings.TrimSpace(track.Title) == "" ||
			strings.TrimSpace(track.Artist) == "" || track.DurationMs < 0 {
			return nil, fmt.Errorf("library catalog contains an invalid track")
		}
		parsedURL, err := url.Parse(track.URL)
		if err != nil || parsedURL == nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") ||
			parsedURL.Hostname() == "" || parsedURL.User != nil || parsedURL.Opaque != "" {
			return nil, fmt.Errorf("track %q has an invalid audio URL", track.ID)
		}
		if _, exists := index[track.ID]; exists {
			return nil, fmt.Errorf("library catalog contains duplicate track ID %q", track.ID)
		}
		index[track.ID] = track
	}
	return index, nil
}
