package ringbuf

import (
	"bytes"
	"testing"
)

func TestRingBufferBasicWrite(t *testing.T) {
	rb := NewRingBuffer(100)
	rb.Write([]byte("hello"))
	if got := rb.Bytes(); !bytes.Equal(got, []byte("hello")) {
		t.Fatalf("expected %q, got %q", "hello", got)
	}
	if rb.Len() != 5 {
		t.Fatalf("expected len 5, got %d", rb.Len())
	}
}

func TestRingBufferOverflow(t *testing.T) {
	rb := NewRingBuffer(10)
	rb.Write([]byte("12345"))
	rb.Write([]byte("67890"))
	// Buffer is exactly at capacity
	if got := rb.Bytes(); !bytes.Equal(got, []byte("1234567890")) {
		t.Fatalf("expected %q, got %q", "1234567890", got)
	}

	// One more byte pushes oldest out
	rb.Write([]byte("X"))
	if got := rb.Bytes(); !bytes.Equal(got, []byte("234567890X")) {
		t.Fatalf("expected %q, got %q", "234567890X", got)
	}
}

func TestRingBufferLargeWrite(t *testing.T) {
	rb := NewRingBuffer(5)
	rb.Write([]byte("abcdefghij")) // 10 bytes, cap is 5
	if got := rb.Bytes(); !bytes.Equal(got, []byte("fghij")) {
		t.Fatalf("expected %q, got %q", "fghij", got)
	}
	if rb.Len() != 5 {
		t.Fatalf("expected len 5, got %d", rb.Len())
	}
}

func TestRingBufferMultipleWrites(t *testing.T) {
	rb := NewRingBuffer(8)
	rb.Write([]byte("aaa"))
	rb.Write([]byte("bbb"))
	rb.Write([]byte("ccc"))
	// Total 9 bytes, cap 8 -> oldest byte trimmed
	if got := rb.Bytes(); !bytes.Equal(got, []byte("aabbbccc")) {
		t.Fatalf("expected %q, got %q", "aabbbccc", got)
	}
}

func TestRingBufferReset(t *testing.T) {
	rb := NewRingBuffer(100)
	rb.Write([]byte("data"))
	rb.Reset()
	if rb.Len() != 0 {
		t.Fatalf("expected len 0 after reset, got %d", rb.Len())
	}
	if got := rb.Bytes(); got != nil {
		t.Fatalf("expected nil after reset, got %q", got)
	}
}

func TestRingBufferBytesReturnsCopy(t *testing.T) {
	rb := NewRingBuffer(100)
	rb.Write([]byte("original"))
	got := rb.Bytes()
	// Mutate the returned slice
	got[0] = 'X'
	// Buffer should be unchanged
	if buf := rb.Bytes(); buf[0] != 'o' {
		t.Fatalf("Bytes() did not return a copy; buffer was mutated")
	}
}

func TestRingBufferEmptyWrite(t *testing.T) {
	rb := NewRingBuffer(10)
	rb.Write(nil)
	rb.Write([]byte{})
	if rb.Len() != 0 {
		t.Fatalf("expected len 0 after empty writes, got %d", rb.Len())
	}
}
