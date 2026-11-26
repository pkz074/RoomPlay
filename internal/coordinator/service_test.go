package coordinator

import (
	"encoding/json"
	"io"
	"log"
	"testing"

	"github.com/pkz074/RoomPlay/internal/protocol"
)

func TestRegisterReply(t *testing.T) {
	reg, _, _ := newTest()
	svc := NewService(nil, reg, log.New(io.Discard, "", 0))

	var ok protocol.RegistrationResponse
	raw := svc.registerReply([]byte(`{"room_id":"bedroom","name":"Bedroom","instance_id":"f7d4a0"}`))
	if err := json.Unmarshal(raw, &ok); err != nil || !ok.Success || ok.RegistrationToken == "" {
		t.Fatalf("valid request failed: %s", raw)
	}

	for _, bad := range []string{`not json`, `{"room_id":"bedroom","name":"B","instance_id":"x","extra":1}`, `{"room_id":"bedroom","name":"B","instance_id":"x"}]`, `{"room_id":"bedroom","name":"B","instance_id":"x"}}`, ``} {
		var resp protocol.RegistrationResponse
		raw := svc.registerReply([]byte(bad))
		if err := json.Unmarshal(raw, &resp); err != nil || resp.Success || resp.Error == nil {
			t.Errorf("bad input %q was not rejected: %s", bad, raw)
		}
	}

	var dup protocol.RegistrationResponse
	raw = svc.registerReply([]byte(`{"room_id":"bedroom","name":"Bedroom","instance_id":"other"}`))
	json.Unmarshal(raw, &dup)
	if dup.Success || dup.Error.Code != protocol.ErrDuplicateRoomID {
		t.Fatalf("duplicate not rejected: %s", raw)
	}
}
