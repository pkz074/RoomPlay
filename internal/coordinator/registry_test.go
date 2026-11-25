package coordinator

import (
	"sync"
	"testing"
	"time"

	"github.com/pkz074/RoomPlay/internal/protocol"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	f.t = f.t.Add(d)
	f.mu.Unlock()
}

type recorder struct {
	mu      sync.Mutex
	changes []Change
}

func (r *recorder) add(c Change) {
	r.mu.Lock()
	r.changes = append(r.changes, c)
	r.mu.Unlock()
}

func (r *recorder) last() (Change, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.changes) == 0 {
		return Change{}, false
	}
	return r.changes[len(r.changes)-1], true
}

func newTest() (*Registry, *fakeClock, *recorder) {
	clk := &fakeClock{t: time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)}
	rec := &recorder{}
	return NewRegistry(clk, rec.add), clk, rec
}

func reqFor(room, instance string) protocol.RegistrationRequest {
	return protocol.RegistrationRequest{RoomID: room, Name: "Room " + room, InstanceID: instance}
}

func hbFor(room, instance, token string) protocol.Heartbeat {
	return protocol.Heartbeat{RoomID: room, InstanceID: instance, RegistrationToken: token}
}

func TestInitialRegistration(t *testing.T) {
	reg, _, rec := newTest()
	resp := reg.Register(reqFor("bedroom", "i1"))
	if !resp.Success || resp.Error != nil {
		t.Fatalf("registration failed: %+v", resp)
	}
	if resp.RegistrationToken == "" || resp.HeartbeatIntervalMs != 2000 || resp.RoomID != "bedroom" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	room, ok := reg.Get("bedroom")
	if !ok || !room.Online || room.InstanceID != "i1" || room.Name != "Room bedroom" {
		t.Fatalf("unexpected room: %+v", room)
	}
	if c, _ := rec.last(); c.RoomID != "bedroom" || c.Availability != protocol.RoomOnline {
		t.Fatalf("missing online change: %+v", c)
	}
}

func TestSameInstanceRenewal(t *testing.T) {
	reg, clk, _ := newTest()
	first := reg.Register(reqFor("bedroom", "i1"))
	clk.Advance(4 * time.Second)
	second := reg.Register(reqFor("bedroom", "i1"))
	if !second.Success || second.RegistrationToken != first.RegistrationToken {
		t.Fatalf("renewal should succeed and keep token: %+v", second)
	}
	clk.Advance(4 * time.Second) // 8s after first, only 4s after renewal
	reg.Sweep()
	if room, _ := reg.Get("bedroom"); !room.Online {
		t.Fatal("renewal did not refresh the lease")
	}
}

func TestDuplicateLiveRoomRejected(t *testing.T) {
	reg, _, _ := newTest()
	reg.Register(reqFor("bedroom", "i1"))
	resp := reg.Register(reqFor("bedroom", "i2"))
	if resp.Success || resp.Error == nil || resp.Error.Code != protocol.ErrDuplicateRoomID {
		t.Fatalf("duplicate must be rejected: %+v", resp)
	}
	if room, _ := reg.Get("bedroom"); room.InstanceID != "i1" {
		t.Fatalf("original instance was replaced: %+v", room)
	}
}

func TestExpiry(t *testing.T) {
	reg, clk, rec := newTest()
	reg.Register(reqFor("bedroom", "i1"))
	clk.Advance(5900 * time.Millisecond)
	reg.Sweep()
	if room, _ := reg.Get("bedroom"); !room.Online {
		t.Fatal("expired too early")
	}
	clk.Advance(100 * time.Millisecond) // exactly 6s
	reg.Sweep()
	if room, _ := reg.Get("bedroom"); room.Online {
		t.Fatal("should be offline after 6s")
	}
	if c, _ := rec.last(); c.Availability != protocol.RoomOffline {
		t.Fatalf("missing offline change: %+v", c)
	}
}

func TestHeartbeatKeepsRoomAlive(t *testing.T) {
	reg, clk, _ := newTest()
	resp := reg.Register(reqFor("bedroom", "i1"))
	for i := 0; i < 10; i++ {
		clk.Advance(2 * time.Second)
		if err := reg.Heartbeat(hbFor("bedroom", "i1", resp.RegistrationToken)); err != nil {
			t.Fatal(err)
		}
		reg.Sweep()
	}
	if room, _ := reg.Get("bedroom"); !room.Online {
		t.Fatal("room should still be online")
	}
}

func TestStaleHeartbeatsIgnored(t *testing.T) {
	reg, clk, _ := newTest()
	resp := reg.Register(reqFor("bedroom", "i1"))
	before, _ := reg.Get("bedroom")
	clk.Advance(2 * time.Second)

	if err := reg.Heartbeat(hbFor("bedroom", "i1", "wrong-token")); err != ErrStaleHeartbeat {
		t.Fatalf("wrong token: got %v", err)
	}
	if err := reg.Heartbeat(hbFor("bedroom", "other", resp.RegistrationToken)); err != ErrStaleHeartbeat {
		t.Fatalf("wrong instance: got %v", err)
	}
	if err := reg.Heartbeat(hbFor("nope", "i1", resp.RegistrationToken)); err != ErrUnknownRoom {
		t.Fatalf("unknown room: got %v", err)
	}
	after, _ := reg.Get("bedroom")
	if !after.LastHeartbeat.Equal(before.LastHeartbeat) {
		t.Fatal("rejected heartbeat must not update last-seen time")
	}
}

func TestLateHeartbeatAfterExpiry(t *testing.T) {
	reg, clk, _ := newTest()
	resp := reg.Register(reqFor("bedroom", "i1"))
	clk.Advance(7 * time.Second) // no Sweep: Heartbeat must expire lazily
	if err := reg.Heartbeat(hbFor("bedroom", "i1", resp.RegistrationToken)); err != ErrRoomOffline {
		t.Fatalf("late heartbeat: got %v", err)
	}
	if room, _ := reg.Get("bedroom"); room.Online {
		t.Fatal("room must stay offline until it re-registers")
	}
	// Re-registering the same instance brings it back.
	if r := reg.Register(reqFor("bedroom", "i1")); !r.Success {
		t.Fatalf("re-register failed: %+v", r)
	}
	if room, _ := reg.Get("bedroom"); !room.Online {
		t.Fatal("room should be online after re-register")
	}
}

func TestReplacementAfterExpiry(t *testing.T) {
	reg, clk, _ := newTest()
	old := reg.Register(reqFor("bedroom", "i1"))
	clk.Advance(6 * time.Second)
	repl := reg.Register(reqFor("bedroom", "i2"))
	if !repl.Success || repl.RegistrationToken == old.RegistrationToken {
		t.Fatalf("replacement should succeed with a new token: %+v", repl)
	}
	if room, _ := reg.Get("bedroom"); room.InstanceID != "i2" || !room.Online {
		t.Fatalf("unexpected room: %+v", room)
	}
	// The old instance's heartbeat is now fenced out.
	if err := reg.Heartbeat(hbFor("bedroom", "i1", old.RegistrationToken)); err != ErrStaleHeartbeat {
		t.Fatalf("old instance heartbeat: got %v", err)
	}
}

func TestInvalidRegistrations(t *testing.T) {
	reg, _, _ := newTest()
	bad := []protocol.RegistrationRequest{
		{RoomID: "", Name: "x", InstanceID: "i"},
		{RoomID: "Bad.Room", Name: "x", InstanceID: "i"},
		{RoomID: "rooms.*", Name: "x", InstanceID: "i"},
		{RoomID: "ok", Name: "x", InstanceID: ""},
		{RoomID: "ok", Name: "   ", InstanceID: "i"},
	}
	for _, b := range bad {
		resp := reg.Register(b)
		if resp.Success || resp.Error == nil || resp.Error.Code != protocol.ErrInvalidPayload {
			t.Errorf("accepted invalid request %+v -> %+v", b, resp)
		}
	}
	if len(reg.List()) != 0 {
		t.Fatal("invalid requests must not create rooms")
	}
}

func TestThreeRoomsIndependent(t *testing.T) {
	reg, clk, _ := newTest()
	tokens := map[string]string{}
	for _, id := range []string{"bedroom", "kitchen", "livingroom"} {
		tokens[id] = reg.Register(reqFor(id, id+"-1")).RegistrationToken
	}
	clk.Advance(4 * time.Second)
	reg.Heartbeat(hbFor("bedroom", "bedroom-1", tokens["bedroom"]))
	reg.Heartbeat(hbFor("livingroom", "livingroom-1", tokens["livingroom"]))
	clk.Advance(3 * time.Second) // kitchen is now 7s silent
	reg.Sweep()
	k, _ := reg.Get("kitchen")
	b, _ := reg.Get("bedroom")
	if k.Online || !b.Online {
		t.Fatalf("kitchen should be offline, bedroom online: %+v %+v", k, b)
	}
}

func TestConcurrentAccess(t *testing.T) {
	reg, _, _ := newTest()
	resp := reg.Register(reqFor("bedroom", "i1"))
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				reg.Heartbeat(hbFor("bedroom", "i1", resp.RegistrationToken))
				reg.Register(reqFor("bedroom", "i1"))
				reg.Register(reqFor("bedroom", "intruder"))
				reg.Sweep()
				reg.List()
			}
		}()
	}
	wg.Wait()
}
