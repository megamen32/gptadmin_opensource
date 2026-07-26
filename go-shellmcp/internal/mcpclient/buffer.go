package mcpclient

import "sync"

const childStderrLimit = 64 << 10

// boundedBuffer retains only the newest child diagnostics so a noisy child
// cannot make the long-lived ShellMCP process consume unbounded memory.
type boundedBuffer struct {
	mu   sync.Mutex
	data []byte
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(p) >= childStderrLimit {
		b.data = append(b.data[:0], p[len(p)-childStderrLimit:]...)
		return len(p), nil
	}
	if overflow := len(b.data) + len(p) - childStderrLimit; overflow > 0 {
		copy(b.data, b.data[overflow:])
		b.data = b.data[:len(b.data)-overflow]
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

func (b *boundedBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.data)
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data)
}
