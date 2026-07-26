package relay

import (
	"bytes"
	"errors"
	"testing"
)

func TestFrameCodecAcceptsOnlyBoundedV1Frames(t *testing.T) {
	tests := []Frame{
		{Type: FrameData, Payload: []byte("hello")},
		{Type: FrameFIN},
		{Type: FrameReset, Payload: []byte("peer_reset")},
		{Type: FrameError, Payload: []byte("invalid_frame")},
	}
	for _, want := range tests {
		raw, err := EncodeFrame(want, 64)
		if err != nil {
			t.Fatalf("encode %v: %v", want.Type, err)
		}
		got, err := DecodeFrame(raw, 64)
		if err != nil {
			t.Fatalf("decode %v: %v", want.Type, err)
		}
		if got.Type != want.Type || !bytes.Equal(got.Payload, want.Payload) {
			t.Fatalf("frame = %#v, want %#v", got, want)
		}
	}

	if _, err := DecodeFrame([]byte{0xff}, 64); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("unknown frame error = %v", err)
	}
	if _, err := DecodeFrame(append([]byte{byte(FrameData)}, make([]byte, 65)...), 64); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("oversized frame error = %v", err)
	}
	if _, err := DecodeFrame([]byte{byte(FrameFIN), 1}, 64); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("FIN payload error = %v", err)
	}
}
