package pty

// Session represents a pseudo-terminal session wrapping a shell process.
type Session interface {
	// Start launches the given command in a new PTY.
	Start(cmd string, args ...string) error
	// Read reads output from the PTY.
	Read(buf []byte) (int, error)
	// Write sends input to the PTY.
	Write(data []byte) (int, error)
	// Resize changes the PTY window size.
	Resize(rows, cols uint16) error
	// Close terminates the PTY session and cleans up.
	Close() error
	// Pid returns the process ID of the running command.
	Pid() int
}
