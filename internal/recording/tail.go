package recording

import "sync"

// byteTail is an io.Writer that retains only the newest bytes written to it.
// FFmpeg diagnostics are useful when an encode fails, but they must not be
// allowed to grow with the duration of a recording.
type byteTail struct {
	mu    sync.Mutex
	limit int
	data  []byte
}

func newByteTail(limit int) *byteTail {
	if limit < 1 {
		limit = 1
	}
	return &byteTail{limit: limit, data: make([]byte, 0, limit)}
}

func (t *byteTail) Write(p []byte) (int, error) {
	originalLength := len(p)
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(p) >= t.limit {
		t.data = append(t.data[:0], p[len(p)-t.limit:]...)
		return originalLength, nil
	}
	overflow := len(t.data) + len(p) - t.limit
	if overflow > 0 {
		copy(t.data, t.data[overflow:])
		t.data = t.data[:len(t.data)-overflow]
	}
	t.data = append(t.data, p...)
	return originalLength, nil
}

func (t *byteTail) String() string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.data)
}
