package library

import (
	"strings"
	"testing"
)

func TestLoadCatalog(t *testing.T) {
	input := `{"tracks":[{"id":"track-001","title":"Demo","artist":"Artist","duration_ms":210000,"audio_file":"track-001.mp3","source":"https://example.com/source","license":"CC0-1.0","license_url":"https://creativecommons.org/publicdomain/zero/1.0/","attribution":"Artist"}]}`

	catalog, err := LoadCatalog(strings.NewReader(input), "http://192.0.2.10:8090/")
	if err != nil {
		t.Fatalf("LoadCatalog() error = %v", err)
	}

	tracks := catalog.Tracks()
	if len(tracks) != 1 {
		t.Fatalf("Tracks() length = %d, want 1", len(tracks))
	}
	if tracks[0].URL != "http://192.0.2.10:8090/audio/track-001.mp3" {
		t.Errorf("Track.URL = %q", tracks[0].URL)
	}
}

func TestLoadCatalogRejectsInvalidEntries(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "duplicate id", input: `{"tracks":[{"id":"x","title":"A","artist":"B","duration_ms":1,"audio_file":"a.mp3","source":"source","license":"license","license_url":"license-url","attribution":"attribution"},{"id":"x","title":"C","artist":"D","duration_ms":2,"audio_file":"b.mp3","source":"source","license":"license","license_url":"license-url","attribution":"attribution"}]}`},
		{name: "negative duration", input: `{"tracks":[{"id":"x","title":"A","artist":"B","duration_ms":-1,"audio_file":"a.mp3","source":"source","license":"license","license_url":"license-url","attribution":"attribution"}]}`},
		{name: "traversal filename", input: `{"tracks":[{"id":"x","title":"A","artist":"B","duration_ms":1,"audio_file":"../a.mp3","source":"source","license":"license","license_url":"license-url","attribution":"attribution"}]}`},
		{name: "unknown field", input: `{"tracks":[{"unknown":true}]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := LoadCatalog(strings.NewReader(tt.input), "http://localhost:8090"); err == nil {
				t.Fatal("LoadCatalog() error = nil, want rejection")
			}
		})
	}
}
