package protocol

// NATS subjects. Do not redefine these in individual components.
const (
	SubjectRegister  = "roomplay.rooms.register"
	SubjectHeartbeat = "roomplay.rooms.heartbeat"
	SubjectAllState  = "roomplay.rooms.*.state"
)

func CommandSubject(roomID string) string { return "roomplay.rooms." + roomID + ".command" }
func GetStateSubject(roomID string) string { return "roomplay.rooms." + roomID + ".get_state" }
func StateSubject(roomID string) string { return "roomplay.rooms." + roomID + ".state" }
