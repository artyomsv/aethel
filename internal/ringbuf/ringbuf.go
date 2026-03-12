package ringbuf

import "sync"

// RingBuffer is a thread-safe circular byte buffer that keeps the most
// recent data when capacity is exceeded. Used to buffer PTY output for
// replay on TUI reconnect.
type RingBuffer struct {
	data []byte
	cap  int
	mu   sync.Mutex
}

// NewRingBuffer creates a ring buffer with the given byte capacity.
func NewRingBuffer(capacity int) *RingBuffer {
	return &RingBuffer{
		cap: capacity,
	}
}

// Write appends p to the buffer. If the total exceeds capacity, the
// oldest bytes are trimmed from the front.
func (rb *RingBuffer) Write(p []byte) {
	if len(p) == 0 {
		return
	}
	rb.mu.Lock()
	defer rb.mu.Unlock()

	// If the incoming write alone exceeds capacity, keep only the tail
	if len(p) >= rb.cap {
		rb.data = make([]byte, rb.cap)
		copy(rb.data, p[len(p)-rb.cap:])
		return
	}

	rb.data = append(rb.data, p...)

	// Trim front if over capacity, compact to release old backing array
	if len(rb.data) > rb.cap {
		trim := len(rb.data) - rb.cap
		compacted := make([]byte, rb.cap)
		copy(compacted, rb.data[trim:])
		rb.data = compacted
	}
}

// Bytes returns a copy of all buffered data.
func (rb *RingBuffer) Bytes() []byte {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	if len(rb.data) == 0 {
		return nil
	}
	out := make([]byte, len(rb.data))
	copy(out, rb.data)
	return out
}

// Len returns the current number of bytes in the buffer.
func (rb *RingBuffer) Len() int {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return len(rb.data)
}

// Reset clears all buffered data.
func (rb *RingBuffer) Reset() {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.data = nil
}
