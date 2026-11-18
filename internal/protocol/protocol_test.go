package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRoomIDValidation(t *testing.T) {
	for _, tc := range []struct {
		id   string
		want bool
	}{
		{"bedroom", true}, {"living-room_1", true},
		{"", false}, {"Bedroom", false}, {"a.b", false},
		{"rooms.*", false}, {"with space", false}, {"-bad", false},
		{strings.Repeat("a", 129), false},
	} {
		if got := ValidRoomID(tc.id); got != tc.want {
			t.Errorf("ValidRoomID(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
}

func TestCommandValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		kind    CommandType
		payload string
		valid   bool
	}{
		{"play", PlaybackPlay, `{"track_id":"t1"}`, true},
		{"add", QueueAdd, `{"track_id":"t1"}`, true},
		{"remove", QueueRemove, `{"item_id":"i1"}`, true},
		{"pause", PlaybackPause, `{}`, true},
		{"unknown", "playback.shuffle", `{}`, false},
		{"missing", QueueAdd, `{}`, false},
		{"extra field", PlaybackNext, `{"track_id":"t1"}`, false},
		{"null", PlaybackNext, `null`, false},
		{"array", PlaybackPause, `[]`, false},
		{"malformed", PlaybackPlay, `{`, false},
		{"multiple objects", PlaybackPause, `{}{}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := CommandRequest{RequestID: "cmd-1", Type: tc.kind, Payload: json.RawMessage(tc.payload)}
			if err := ValidateCommand(c); (err == nil) != tc.valid {
				t.Fatalf("ValidateCommand(%+v) = %v, valid=%v", c, err, tc.valid)
			}
		})
	}
}

func TestStoppedStateJSONHasEmptyArrays(t *testing.T) {
	b, err := json.Marshal(NewPlaybackState())
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{`"history":[]`, `"queue":[]`, `"current":null`} {
		if !strings.Contains(string(b), part) {
			t.Fatalf("missing %s in %s", part, b)
		}
	}
}

func TestHTTPStatus(t *testing.T) {
	for code, want := range map[ErrorCode]int{
		ErrInvalidPayload: 400, ErrTrackNotFound: 404, ErrInvalidState: 409,
		ErrRoomOffline: 503, ErrCommandTimeout: 504, ErrAudio: 500,
	} {
		if got := HTTPStatus(code); got != want {
			t.Errorf("%s => %d, want %d", code, got, want)
		}
	}
}
