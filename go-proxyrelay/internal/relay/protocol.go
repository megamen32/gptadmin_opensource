package relay

import "errors"

const ProtocolVersion = 1

// FrameType identifies one v1 relay frame operation.
type FrameType byte

const (
	FrameData FrameType = iota + 1
	FrameFIN
	FrameReset
	FrameError
)

var (
	ErrInvalidFrame  = errors.New("relay frame is invalid")
	ErrFrameTooLarge = errors.New("relay frame payload is too large")
)

// Frame is one bounded relay protocol message.
type Frame struct {
	Type    FrameType
	Payload []byte
}

// EncodeFrame encodes one v1 frame while enforcing its payload limit.
func EncodeFrame(frame Frame, maxPayloadBytes int64) ([]byte, error) {
	if !validFrameType(frame.Type) || maxPayloadBytes < 0 {
		return nil, ErrInvalidFrame
	}
	if int64(len(frame.Payload)) > maxPayloadBytes {
		return nil, ErrFrameTooLarge
	}
	if frame.Type == FrameFIN && len(frame.Payload) != 0 {
		return nil, ErrInvalidFrame
	}
	raw := make([]byte, len(frame.Payload)+1)
	raw[0] = byte(frame.Type)
	copy(raw[1:], frame.Payload)
	return raw, nil
}

// DecodeFrame decodes one complete v1 frame while enforcing its payload limit.
func DecodeFrame(raw []byte, maxPayloadBytes int64) (Frame, error) {
	if len(raw) == 0 || maxPayloadBytes < 0 {
		return Frame{}, ErrInvalidFrame
	}
	frame := Frame{Type: FrameType(raw[0])}
	if !validFrameType(frame.Type) {
		return Frame{}, ErrInvalidFrame
	}
	if int64(len(raw)-1) > maxPayloadBytes {
		return Frame{}, ErrFrameTooLarge
	}
	if frame.Type == FrameFIN && len(raw) != 1 {
		return Frame{}, ErrInvalidFrame
	}
	frame.Payload = append([]byte(nil), raw[1:]...)
	return frame, nil
}

func validFrameType(frameType FrameType) bool {
	return frameType >= FrameData && frameType <= FrameError
}
