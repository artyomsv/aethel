//go:build windows

package pty

import (
	"io"
	"syscall"

	"github.com/charmbracelet/x/conpty"
)

type winSession struct {
	cpty   *conpty.ConPty
	pid    int
	handle uintptr
}

func New() Session {
	return &winSession{}
}

func (s *winSession) Start(cmd string, args ...string) error {
	cp, err := conpty.New(80, 24, 0)
	if err != nil {
		return err
	}
	s.cpty = cp

	fullArgs := append([]string{cmd}, args...)
	pid, handle, err := cp.Spawn(cmd, fullArgs, &syscall.ProcAttr{})
	if err != nil {
		cp.Close()
		return err
	}
	s.pid = pid
	s.handle = handle
	return nil
}

func (s *winSession) Read(buf []byte) (int, error) {
	if s.cpty == nil {
		return 0, io.EOF
	}
	return s.cpty.Read(buf)
}

func (s *winSession) Write(data []byte) (int, error) {
	if s.cpty == nil {
		return 0, io.ErrClosedPipe
	}
	return s.cpty.Write(data)
}

func (s *winSession) Resize(rows, cols uint16) error {
	if s.cpty == nil {
		return nil
	}
	return s.cpty.Resize(int(cols), int(rows))
}

func (s *winSession) Close() error {
	if s.cpty != nil {
		s.cpty.Close()
	}
	return nil
}

func (s *winSession) Pid() int {
	return s.pid
}
