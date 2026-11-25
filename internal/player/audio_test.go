package player

import (
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pkz074/RoomPlay/internal/protocol"
)

func TestDecodeAndResampleMP3UsesTheLicensedCatalogAsset(t *testing.T) {
	asset := filepath.Join("..", "..", "assets", "music", "Eternity_Arcade_-_Lost_in_a_Dream_(vocal_retro_pop_music_made_with_AI).mp3")
	encoded, err := os.ReadFile(asset)
	if err != nil {
		t.Fatalf("read demo MP3: %v", err)
	}
	pcm, durationMs, err := decodeAndResampleMP3(encoded)
	if err != nil {
		t.Fatalf("decodeAndResampleMP3: %v", err)
	}
	if len(pcm) == 0 || len(pcm)%audioBytesPerFrame != 0 {
		t.Fatalf("decoded PCM size = %d, want non-empty stereo frames", len(pcm))
	}
	if durationMs < 200_000 || durationMs > 240_000 {
		t.Fatalf("decoded duration = %d ms, want near the catalog's 220000 ms", durationMs)
	}
}

func TestResamplePCMInterpolatesStereoSamples(t *testing.T) {
	source := make([]byte, 3*audioBytesPerFrame)
	for frame, sample := range []int16{0, 1000, 2000} {
		leftOffset := frame * audioBytesPerFrame
		binary.LittleEndian.PutUint16(source[leftOffset:leftOffset+2], uint16(sample))
		binary.LittleEndian.PutUint16(source[leftOffset+2:leftOffset+4], uint16(-sample))
	}
	got, err := resamplePCM(source, audioOutputSampleRate/2)
	if err != nil {
		t.Fatalf("resamplePCM: %v", err)
	}
	want := []int16{0, 500, 1000, 1500, 2000, 2000}
	if len(got) != len(want)*audioBytesPerFrame {
		t.Fatalf("resampled size = %d, want %d", len(got), len(want)*audioBytesPerFrame)
	}
	for frame, sample := range want {
		offset := frame * audioBytesPerFrame
		if left := int16(binary.LittleEndian.Uint16(got[offset : offset+2])); left != sample {
			t.Fatalf("left frame %d = %d, want %d", frame, left, sample)
		}
		if right := int16(binary.LittleEndian.Uint16(got[offset+2 : offset+4])); right != -sample {
			t.Fatalf("right frame %d = %d, want %d", frame, right, -sample)
		}
	}
}

func TestResamplePCMRejectsInvalidInput(t *testing.T) {
	for _, test := range []struct {
		name string
		pcm  []byte
		rate int
	}{
		{name: "empty", rate: audioOutputSampleRate},
		{name: "invalid rate", pcm: make([]byte, audioBytesPerFrame)},
		{name: "partial frame", pcm: []byte{1, 2, 3}, rate: audioOutputSampleRate},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := resamplePCM(test.pcm, test.rate); err == nil {
				t.Fatal("resamplePCM accepted invalid PCM")
			}
		})
	}
}

func TestAudioBackendPrepareDownloadsAndDecodesBeforeCreatingPlayer(t *testing.T) {
	asset := filepath.Join("..", "..", "assets", "music", "Eternity_Arcade_-_Lost_in_a_Dream_(vocal_retro_pop_music_made_with_AI).mp3")
	encoded, err := os.ReadFile(asset)
	if err != nil {
		t.Fatalf("read demo MP3: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/audio/demo.mp3" {
			t.Errorf("unexpected audio request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write(encoded)
	}))
	defer server.Close()
	output := &fakeAudioOutput{}
	backend := newAudioBackend(output, server.Client(), newFakePlaybackClock())
	defer backend.Close()
	track := protocol.Track{ID: "demo", DurationMs: 220_000, URL: server.URL + "/audio/demo.mp3"}
	if err := backend.Prepare(context.Background(), track); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if backend.prepared == nil || backend.prepared.trackID != track.ID || backend.prepared.durationMs < 200_000 {
		t.Fatalf("prepared audio = %+v", backend.prepared)
	}
	if len(output.players) != 1 || output.players[0].isPlaying() {
		t.Fatalf("output players = %d; prepared player should not be playing", len(output.players))
	}
}

func TestAudioBackendPreparationFailureLeavesCurrentAudioAlone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "not an MP3")
	}))
	defer server.Close()
	backend := newAudioBackend(&fakeAudioOutput{}, server.Client(), newFakePlaybackClock())
	current := &preparedAudio{
		trackID:    "current",
		pcm:        newSeekablePCM(make([]byte, audioOutputSampleRate*audioBytesPerFrame)),
		player:     &fakeAudioOutputPlayer{},
		durationMs: 1000,
	}
	backend.current = current
	backend.playing = true
	err := backend.Prepare(context.Background(), protocol.Track{ID: "bad", URL: server.URL + "/bad.mp3"})
	if err == nil {
		t.Fatal("Prepare succeeded for invalid MP3 data")
	}
	if backend.current != current || !backend.playing {
		t.Fatalf("failed preparation altered current audio: current=%p playing=%t", backend.current, backend.playing)
	}
}

func TestAudioBackendPlaybackPositionPauseResumeAndStop(t *testing.T) {
	output := &fakeAudioOutput{}
	backend := newAudioBackend(output, nil, newFakePlaybackClock())
	pcm := newSeekablePCM(make([]byte, audioOutputSampleRate*audioBytesPerFrame*2))
	backend.prepared = &preparedAudio{trackID: "track", pcm: pcm, player: output.NewPlayer(pcm), durationMs: 2000}
	track := protocol.Track{ID: "track"}
	backend.Play(track, 0)
	if len(output.players) != 1 || !output.players[0].isPlaying() {
		t.Fatal("prepared audio did not start playing")
	}
	if _, err := io.CopyN(io.Discard, pcm, audioOutputSampleRate*audioBytesPerFrame); err != nil {
		t.Fatalf("advance PCM reader: %v", err)
	}
	output.players[0].setBufferedSize(audioOutputSampleRate * audioBytesPerFrame / 10)
	if got := backend.PositionMs(); got != 900 {
		t.Fatalf("position = %d ms, want 900 ms after output buffering", got)
	}
	if got := backend.Pause(); got != 900 {
		t.Fatalf("paused position = %d ms, want 900 ms", got)
	}
	backend.Resume()
	if !output.players[0].isPlaying() {
		t.Fatal("audio did not resume")
	}
	backend.Stop()
	if got := backend.PositionMs(); got != 0 || output.players[0].isPlaying() {
		t.Fatalf("stop position=%d playing=%t; want zero and stopped", got, output.players[0].isPlaying())
	}
}

func TestAudioBackendSignalsCompletionAfterBufferedAudioDrains(t *testing.T) {
	clock := newFakePlaybackClock()
	output := &fakeAudioOutput{}
	backend := newAudioBackend(output, nil, clock)
	pcm := newSeekablePCM(make([]byte, audioOutputSampleRate*audioBytesPerFrame))
	backend.prepared = &preparedAudio{trackID: "track", pcm: pcm, player: output.NewPlayer(pcm), durationMs: 1000}
	completed := 0
	backend.SetOnCompletion(func() { completed++ })
	backend.Play(protocol.Track{ID: "track"}, 0)
	if _, err := io.Copy(io.Discard, pcm); err != nil {
		t.Fatalf("advance PCM reader: %v", err)
	}
	clock.Advance(time.Second)
	if completed != 0 {
		t.Fatal("completion fired before the device buffer drained")
	}
	output.players[0].setPlaying(false)
	clock.Advance(audioCompletionPoll)
	if completed != 1 {
		t.Fatalf("completion count = %d, want 1", completed)
	}
}

type fakeAudioOutput struct {
	mu      sync.Mutex
	players []*fakeAudioOutputPlayer
}

func (o *fakeAudioOutput) NewPlayer(source io.Reader) audioOutputPlayer {
	player := &fakeAudioOutputPlayer{source: source}
	o.mu.Lock()
	o.players = append(o.players, player)
	o.mu.Unlock()
	return player
}

type fakeAudioOutputPlayer struct {
	mu           sync.Mutex
	source       io.Reader
	playing      bool
	bufferedSize int
	playCount    int
	pauseCount   int
	err          error
}

func (p *fakeAudioOutputPlayer) Play() {
	p.mu.Lock()
	p.playing = true
	p.playCount++
	p.mu.Unlock()
}

func (p *fakeAudioOutputPlayer) PauseAndStopReading() {
	p.mu.Lock()
	p.playing = false
	p.pauseCount++
	p.mu.Unlock()
}

func (p *fakeAudioOutputPlayer) IsPlaying() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.playing
}

func (p *fakeAudioOutputPlayer) BufferedSize() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.bufferedSize
}

func (p *fakeAudioOutputPlayer) Seek(offset int64, whence int) (int64, error) {
	seeker, ok := p.source.(io.Seeker)
	if !ok {
		return 0, io.ErrUnexpectedEOF
	}
	return seeker.Seek(offset, whence)
}

func (p *fakeAudioOutputPlayer) Err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

func (p *fakeAudioOutputPlayer) setBufferedSize(size int) {
	p.mu.Lock()
	p.bufferedSize = size
	p.mu.Unlock()
}

func (p *fakeAudioOutputPlayer) setPlaying(playing bool) {
	p.mu.Lock()
	p.playing = playing
	p.mu.Unlock()
}

func (p *fakeAudioOutputPlayer) isPlaying() bool {
	return p.IsPlaying()
}

var _ audioOutput = (*fakeAudioOutput)(nil)
var _ audioOutputPlayer = (*fakeAudioOutputPlayer)(nil)