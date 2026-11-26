package coordinator

import (
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/pkz074/RoomPlay/internal/protocol"
)

func TestAvailabilityNotificationsPreserveMutationOrder(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{false: "renewal", true: "replacement"}[replace], func(t *testing.T) {
			clock := &fakeClock{t: time.Now()}
			recorded := &recorder{}
			offlineEntered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			var reg *Registry
			reg = NewRegistry(clock, func(change Change) {
				// Callback reads must remain safe while notifications are draining.
				reg.Get(change.RoomID)
				if change.Availability == protocol.RoomOffline {
					close(offlineEntered)
					<-release
				}
				recorded.add(change)
			})
			if response := reg.Register(reqFor("bedroom", "i1")); !response.Success {
				t.Fatalf("initial registration: %+v", response)
			}
			clock.Advance(HeartbeatTimeout)
			sweepDone := make(chan struct{})
			go func() { reg.Sweep(); close(sweepDone) }()
			awaitRegistry(t, offlineEntered)
			instance := "i1"
			if replace {
				instance = "i2"
			}
			renewed := make(chan protocol.RegistrationResponse, 1)
			go func() { renewed <- reg.Register(reqFor("bedroom", instance)) }()
			if response := awaitRegistry(t, renewed); !response.Success {
				t.Fatalf("registration while offline callback is blocked: %+v", response)
			}
			unblock()
			awaitRegistry(t, sweepDone)
			recorded.mu.Lock()
			got := append([]Change(nil), recorded.changes...)
			recorded.mu.Unlock()
			want := []Change{{"bedroom", protocol.RoomOnline}, {"bedroom", protocol.RoomOffline}, {"bedroom", protocol.RoomOnline}}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("notifications = %v, want %v", got, want)
			}
			if room, _ := reg.Get("bedroom"); !room.Online || room.InstanceID != instance {
				t.Fatalf("final room = %+v", room)
			}
		})
	}
}

func TestAvailabilityCallbackCanRenewRegistration(t *testing.T) {
	clock := &fakeClock{t: time.Now()}
	var reg *Registry
	var changes []Change
	reg = NewRegistry(clock, func(change Change) {
		changes = append(changes, change)
		if change.Availability == protocol.RoomOffline {
			reg.Register(reqFor("bedroom", "i1"))
		}
	})
	reg.Register(reqFor("bedroom", "i1"))
	clock.Advance(HeartbeatTimeout)
	done := make(chan struct{})
	go func() { reg.Sweep(); close(done) }()
	awaitRegistry(t, done)
	want := []Change{{"bedroom", protocol.RoomOnline}, {"bedroom", protocol.RoomOffline}, {"bedroom", protocol.RoomOnline}}
	if !reflect.DeepEqual(changes, want) {
		t.Fatalf("reentrant notifications = %v, want %v", changes, want)
	}
}

func awaitRegistry[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("registry operation deadlocked")
		var zero T
		return zero
	}
}
