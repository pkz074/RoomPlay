package player

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ebitengine/oto/v3"
	"github.com/hajimehoshi/go-mp3"
	"github.com/pkz074/RoomPlay/internal/protocol"
)

const (
	audioOutputSampleRate  = 44100
	audioOutputChannels    = 2
	audioBytesPerFrame     = audioOutputChannels * 2
	maxAudioDownloadBytes  = 32 << 20
	maxDecodedAudioBytes   = 128 << 20
	audioPrepareTimeout    = 15 * time.Second
	audioCompletionPoll    = 25 * time.Millisecond
	audioMaximumDrainDelay = 250 * time.Millisecond
)

type audioOutput interface {
	NewPlayer(io.Reader) audioOutputPlayer
}

type audioOutputPlayer interface {
	Play()
	PauseAndStopReading()
	IsPlaying() bool
	BufferedSize() int
	Seek(int64, int) (int64, error)
	Err() error
}

type otoAudioOutput struct {
	context oto.Context
}

func (o otoAudioOutput) NewPlayer(source io.Reader) audioOutputPlayer {
	return o.context.NewPlayer(source)
}

// AudioBackend downloads, decodes, and plays catalog-listed MP3 audio.
type AudioBackend struct {
	mu         sync.Mutex
	output     audioOutput
	client     *http.Client
	clock      playbackClock
	current    *preparedAudio
	prepared   *preparedAudio
	baseMs     int64
	playing    bool
	closed     bool
	timer      playbackTimer
	generation uint64
	onComplete func()
}

type preparedAudio struct {
	trackID    string
	pcm        *seekablePCM
	player     audioOutputPlayer
	durationMs int64
}

type seekablePCM struct {
	mu     sync.Mutex
	reader *bytes.Reader
	size   int64
}

// NewAudioBackend initializes the process audio device and returns a real MP3 playback backend.
func NewAudioBackend(ctx context.Context, client *http.Client) (*AudioBackend, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	outputContext, ready, err := oto.NewContext(&oto.NewContextOptions{
		SampleRate:   audioOutputSampleRate,
		ChannelCount: audioOutputChannels,
		Format:       oto.FormatSignedInt16LE,
		BufferSize:   100 * time.Millisecond,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize audio output: %w", err)
	}
	select {
	case <-ready:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := outputContext.Err(); err != nil {
		return nil, fmt.Errorf("initialize audio output: %w", err)
	}
	return newAudioBackend(otoAudioOutput{context: outputContext}, client, systemPlaybackClock{}), nil
}

func newAudioBackend(output audioOutput, client *http.Client, clock playbackClock) *AudioBackend {
	if client == nil {
		client = &http.Client{Timeout: audioPrepareTimeout}
	}
	if clock == nil {
		clock = systemPlaybackClock{}
	}
	return &AudioBackend{output: output, client: client, clock: clock}
}

func (b *AudioBackend) SetOnCompletion(callback func()) {
	b.mu.Lock()
	b.onComplete = callback
	b.mu.Unlock()
}

func (b *AudioBackend) Prepare(ctx context.Context, track protocol.Track) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if !protocol.ValidIdentifier(track.ID) || track.DurationMs < 0 {
		return fmt.Errorf("invalid track metadata")
	}
	parsedURL, err := url.Parse(strings.TrimSpace(track.URL))
	if err != nil || parsedURL == nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") ||
		parsedURL.Hostname() == "" || parsedURL.User != nil || parsedURL.Opaque != "" || parsedURL.Fragment != "" {
		return fmt.Errorf("invalid audio URL")
	}
	b.mu.Lock()
	closed := b.closed
	b.mu.Unlock()
	if closed {
		return fmt.Errorf("audio backend is closed")
	}
	if b.output == nil {
		return fmt.Errorf("audio output is unavailable")
	}

	prepareCtx, cancel := context.WithTimeout(ctx, audioPrepareTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(prepareCtx, http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return fmt.Errorf("create audio request: %w", err)
	}
	response, err := b.client.Do(request)
	if err != nil {
		return fmt.Errorf("download track audio: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("audio endpoint returned HTTP %d", response.StatusCode)
	}
	if response.ContentLength > maxAudioDownloadBytes {
		return fmt.Errorf("audio file exceeds %d bytes", maxAudioDownloadBytes)
	}
	encoded, err := io.ReadAll(io.LimitReader(response.Body, maxAudioDownloadBytes+1))
	if err != nil {
		return fmt.Errorf("read track audio: %w", err)
	}
	if len(encoded) == 0 || len(encoded) > maxAudioDownloadBytes {
		return fmt.Errorf("audio file is empty or exceeds %d bytes", maxAudioDownloadBytes)
	}
	pcmBytes, durationMs, err := decodeAndResampleMP3(encoded)
	if err != nil {
		return fmt.Errorf("decode track audio: %w", err)
	}
	if err := prepareCtx.Err(); err != nil {
		return fmt.Errorf("prepare track audio: %w", err)
	}
	pcm := newSeekablePCM(pcmBytes)
	player := b.output.NewPlayer(pcm)
	if player == nil {
		return fmt.Errorf("create audio output player")
	}
	if err := player.Err(); err != nil {
		player.PauseAndStopReading()
		return fmt.Errorf("prepare audio output: %w", err)
	}
	if _, err := player.Seek(0, io.SeekStart); err != nil {
		player.PauseAndStopReading()
		return fmt.Errorf("prepare audio seek: %w", err)
	}
	candidate := &preparedAudio{trackID: track.ID, pcm: pcm, player: player, durationMs: durationMs}
	b.mu.Lock()
	if b.closed || prepareCtx.Err() != nil {
		ctxErr := prepareCtx.Err()
		b.mu.Unlock()
		candidate.player.PauseAndStopReading()
		if ctxErr != nil {
			return fmt.Errorf("prepare track audio: %w", ctxErr)
		}
		return fmt.Errorf("audio backend is closed")
	}
	oldPrepared := b.prepared
	b.prepared = candidate
	b.mu.Unlock()
	if oldPrepared != nil {
		oldPrepared.player.PauseAndStopReading()
	}
	return nil
}

func (b *AudioBackend) Play(track protocol.Track, positionMs int64) {
	b.mu.Lock()
	if b.closed || b.prepared == nil || b.prepared.trackID != track.ID {
		b.mu.Unlock()
		return
	}
	candidate := b.prepared
	offset := audioOffsetForPosition(positionMs, candidate.durationMs, candidate.pcm.size)
	if _, err := candidate.player.Seek(offset, io.SeekStart); err != nil {
		b.prepared = nil
		b.mu.Unlock()
		candidate.player.PauseAndStopReading()
		return
	}
	b.cancelTimerLocked()
	oldCurrent := b.current
	if oldCurrent != nil {
		oldCurrent.player.PauseAndStopReading()
	}
	b.current = candidate
	b.prepared = nil
	b.baseMs = offset * 1000 / (audioOutputSampleRate * audioBytesPerFrame)
	b.playing = true
	candidate.player.Play()
	b.scheduleCompletionLocked()
	b.mu.Unlock()
}

func (b *AudioBackend) Pause() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.current == nil {
		return 0
	}
	position := b.positionLocked()
	if b.playing {
		b.current.player.PauseAndStopReading()
		position = b.positionLocked()
		b.playing = false
		b.baseMs = position
		b.cancelTimerLocked()
	}
	return b.baseMs
}

func (b *AudioBackend) Resume() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.playing || b.current == nil {
		return
	}
	b.playing = true
	b.current.player.Play()
	b.scheduleCompletionLocked()
}

func (b *AudioBackend) Stop() {
	b.mu.Lock()
	b.cancelTimerLocked()
	current, prepared := b.current, b.prepared
	b.current = nil
	b.prepared = nil
	b.baseMs = 0
	b.playing = false
	b.mu.Unlock()
	if current != nil {
		current.player.PauseAndStopReading()
	}
	if prepared != nil && prepared != current {
		prepared.player.PauseAndStopReading()
	}
}

func (b *AudioBackend) PositionMs() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.playing {
		return b.baseMs
	}
	return b.positionLocked()
}

func (b *AudioBackend) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	b.cancelTimerLocked()
	current, prepared := b.current, b.prepared
	b.current = nil
	b.prepared = nil
	b.playing = false
	b.baseMs = 0
	b.mu.Unlock()
	if current != nil {
		current.player.PauseAndStopReading()
	}
	if prepared != nil && prepared != current {
		prepared.player.PauseAndStopReading()
	}
	return nil
}

func (b *AudioBackend) positionLocked() int64 {
	if b.current == nil {
		return 0
	}
	cursor := b.current.pcm.position()
	buffered := int64(b.current.player.BufferedSize())
	if buffered > 0 {
		if buffered >= cursor {
			cursor = 0
		} else {
			cursor -= buffered
		}
	}
	position := cursor * 1000 / (audioOutputSampleRate * audioBytesPerFrame)
	if position > b.current.durationMs {
		return b.current.durationMs
	}
	return position
}

func (b *AudioBackend) scheduleCompletionLocked() {
	if b.closed || !b.playing || b.current == nil {
		return
	}
	b.cancelTimerLocked()
	remaining := b.current.durationMs - b.positionLocked()
	if remaining < 0 {
		remaining = 0
	}
	delay := time.Duration(remaining) * time.Millisecond
	if delay == 0 {
		delay = time.Millisecond
	}
	generation := b.generation
	b.timer = b.clock.AfterFunc(delay, func() { b.complete(generation) })
}

func (b *AudioBackend) complete(generation uint64) {
	b.mu.Lock()
	if b.closed || !b.playing || b.current == nil || generation != b.generation {
		b.mu.Unlock()
		return
	}
	b.timer = nil
	player := b.current.player
	if player.Err() == nil && b.positionLocked() < b.current.durationMs {
		b.scheduleCompletionLocked()
		b.mu.Unlock()
		return
	}
	if player.Err() == nil && player.IsPlaying() {
		delay := audioCompletionPoll
		if buffered := player.BufferedSize(); buffered > 0 {
			drain := time.Duration(int64(buffered)*1000/(audioOutputSampleRate*audioBytesPerFrame)) * time.Millisecond
			if drain > delay {
				delay = drain
			}
			if delay > audioMaximumDrainDelay {
				delay = audioMaximumDrainDelay
			}
		}
		b.timer = b.clock.AfterFunc(delay, func() { b.complete(generation) })
		b.mu.Unlock()
		return
	}
	callback := b.onComplete
	b.playing = false
	b.baseMs = b.current.durationMs
	b.cancelTimerLocked()
	b.mu.Unlock()
	if callback != nil {
		callback()
	}
}

func (b *AudioBackend) cancelTimerLocked() {
	b.generation++
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
}

func decodeAndResampleMP3(encoded []byte) ([]byte, int64, error) {
	decoder, err := mp3.NewDecoder(bytes.NewReader(encoded))
	if err != nil {
		return nil, 0, fmt.Errorf("invalid MP3 stream: %w", err)
	}
	if decoder.Length() <= 0 || decoder.Length() > maxDecodedAudioBytes {
		return nil, 0, fmt.Errorf("decoded MP3 is empty or exceeds %d bytes", maxDecodedAudioBytes)
	}
	decoded, err := io.ReadAll(io.LimitReader(decoder, maxDecodedAudioBytes+1))
	if err != nil {
		return nil, 0, fmt.Errorf("decode MP3 samples: %w", err)
	}
	if len(decoded) == 0 || len(decoded) > maxDecodedAudioBytes || len(decoded)%audioBytesPerFrame != 0 {
		return nil, 0, fmt.Errorf("decoded MP3 has invalid or oversized PCM data")
	}
	pcm, err := resamplePCM(decoded, decoder.SampleRate())
	if err != nil {
		return nil, 0, err
	}
	frames := int64(len(pcm) / audioBytesPerFrame)
	durationMs := frames * 1000 / audioOutputSampleRate
	if durationMs <= 0 {
		return nil, 0, fmt.Errorf("decoded MP3 has no playable duration")
	}
	return pcm, durationMs, nil
}

func resamplePCM(source []byte, sourceRate int) ([]byte, error) {
	if sourceRate <= 0 || len(source) == 0 || len(source)%audioBytesPerFrame != 0 {
		return nil, fmt.Errorf("invalid decoded PCM format")
	}
	if sourceRate == audioOutputSampleRate {
		if len(source) > maxDecodedAudioBytes {
			return nil, fmt.Errorf("decoded PCM exceeds %d bytes", maxDecodedAudioBytes)
		}
		return source, nil
	}
	sourceFrames := int64(len(source) / audioBytesPerFrame)
	outputFrames := sourceFrames * audioOutputSampleRate / int64(sourceRate)
	if outputFrames <= 0 || outputFrames > maxDecodedAudioBytes/audioBytesPerFrame {
		return nil, fmt.Errorf("resampled PCM is empty or exceeds %d bytes", maxDecodedAudioBytes)
	}
	output := make([]byte, int(outputFrames*audioBytesPerFrame))
	for frame := int64(0); frame < outputFrames; frame++ {
		position := frame * int64(sourceRate)
		leftFrame := position / audioOutputSampleRate
		fraction := position % audioOutputSampleRate
		if leftFrame >= sourceFrames {
			leftFrame = sourceFrames - 1
			fraction = 0
		}
		rightFrame := leftFrame + 1
		if rightFrame >= sourceFrames {
			rightFrame = leftFrame
		}
		for channel := 0; channel < audioOutputChannels; channel++ {
			leftOffset := int(leftFrame*audioBytesPerFrame) + channel*2
			rightOffset := int(rightFrame*audioBytesPerFrame) + channel*2
			left := int64(int16(binary.LittleEndian.Uint16(source[leftOffset : leftOffset+2])))
			right := int64(int16(binary.LittleEndian.Uint16(source[rightOffset : rightOffset+2])))
			sample := (left*(audioOutputSampleRate-fraction) + right*fraction) / audioOutputSampleRate
			outputOffset := int(frame*audioBytesPerFrame) + channel*2
			binary.LittleEndian.PutUint16(output[outputOffset:outputOffset+2], uint16(int16(sample)))
		}
	}
	return output, nil
}

func audioOffsetForPosition(positionMs, durationMs, totalBytes int64) int64 {
	if positionMs <= 0 {
		return 0
	}
	if positionMs >= durationMs {
		return totalBytes
	}
	offset := positionMs * audioOutputSampleRate * audioBytesPerFrame / 1000
	if offset > totalBytes {
		return totalBytes
	}
	return offset - offset%audioBytesPerFrame
}

func newSeekablePCM(data []byte) *seekablePCM {
	return &seekablePCM{reader: bytes.NewReader(data), size: int64(len(data))}
}

func (p *seekablePCM) Read(data []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.reader.Read(data)
}

func (p *seekablePCM) Seek(offset int64, whence int) (int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.reader.Seek(offset, whence)
}

func (p *seekablePCM) position() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	position, _ := p.reader.Seek(0, io.SeekCurrent)
	return position
}

var _ PlaybackBackend = (*AudioBackend)(nil)
var _ audioOutput = otoAudioOutput{}