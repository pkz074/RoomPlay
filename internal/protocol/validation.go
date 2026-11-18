package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

var roomIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

const maxIDLength = 128

func ValidRoomID(id string) bool {
	return len(id) > 0 && len(id) <= maxIDLength && roomIDPattern.MatchString(id)
}

func ValidIdentifier(id string) bool {
	return id != "" && len(id) <= maxIDLength && strings.TrimSpace(id) == id && !strings.ContainsAny(id, "\r\n\x00")
}

// ValidateCommand checks envelope and action-specific JSON, before state-dependent checks.
// It does not access the library or mutate playback state.
func ValidateCommand(command CommandRequest) error {
	if !ValidIdentifier(command.RequestID) {
		return errors.New("invalid request_id")
	}
	var destination any
	switch command.Type {
	case PlaybackPlay, QueueAdd:
		destination = &TrackPayload{}
	case QueueRemove:
		destination = &RemoveItemPayload{}
	case PlaybackPause, PlaybackResume, PlaybackStop, PlaybackNext, PlaybackPrevious, QueueClear:
		destination = &struct{}{}
	default:
		return fmt.Errorf("unknown command type %q", command.Type)
	}

	data := command.Payload
	if len(data) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return errors.New("payload must be a JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("invalid payload: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("payload must contain exactly one JSON object")
	}
	switch value := destination.(type) {
	case *TrackPayload:
		if !ValidIdentifier(value.TrackID) {
			return errors.New("missing or invalid track_id")
		}
	case *RemoveItemPayload:
		if !ValidIdentifier(value.ItemID) {
			return errors.New("missing or invalid item_id")
		}
	}
	return nil
}
