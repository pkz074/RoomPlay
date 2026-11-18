package protocol

type ErrorCode string

const (
	ErrInvalidCommand          ErrorCode = "INVALID_COMMAND"
	ErrInvalidPayload          ErrorCode = "INVALID_PAYLOAD"
	ErrTrackNotFound           ErrorCode = "TRACK_NOT_FOUND"
	ErrQueueItemNotFound       ErrorCode = "QUEUE_ITEM_NOT_FOUND"
	ErrInvalidState            ErrorCode = "INVALID_STATE"
	ErrAudio                   ErrorCode = "AUDIO_ERROR"
	ErrDuplicateRequestConflict ErrorCode = "DUPLICATE_REQUEST_CONFLICT"
	ErrInternal                ErrorCode = "INTERNAL_ERROR"
	ErrRoomNotFound            ErrorCode = "ROOM_NOT_FOUND"
	ErrRoomOffline             ErrorCode = "ROOM_OFFLINE"
	ErrNoResponders            ErrorCode = "NO_RESPONDERS"
	ErrCommandTimeout          ErrorCode = "COMMAND_TIMEOUT"
	ErrBrokerUnavailable       ErrorCode = "BROKER_UNAVAILABLE"
	ErrServiceUnavailable      ErrorCode = "SERVICE_UNAVAILABLE"
	ErrDuplicateRoomID         ErrorCode = "DUPLICATE_ROOM_ID"
)

// HTTPStatus centralizes the documented mapping for transport boundaries.
// Individual services must still choose the correct application or infrastructure error.
func HTTPStatus(code ErrorCode) int {
	switch code {
	case ErrInvalidCommand, ErrInvalidPayload:
		return 400
	case ErrTrackNotFound, ErrQueueItemNotFound, ErrRoomNotFound:
		return 404
	case ErrInvalidState, ErrDuplicateRequestConflict, ErrDuplicateRoomID:
		return 409
	case ErrRoomOffline, ErrNoResponders, ErrBrokerUnavailable, ErrServiceUnavailable:
		return 503
	case ErrCommandTimeout:
		return 504
	default:
		return 500
	}
}
