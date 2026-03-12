//go:build windows

package clipboard

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const cfUnicodeText = 13

var (
	user32           = windows.NewLazySystemDLL("user32.dll")
	kernel32         = windows.NewLazySystemDLL("kernel32.dll")
	openClipboard    = user32.NewProc("OpenClipboard")
	closeClipboard   = user32.NewProc("CloseClipboard")
	getClipboardData = user32.NewProc("GetClipboardData")
	globalLock       = kernel32.NewProc("GlobalLock")
	globalUnlock     = kernel32.NewProc("GlobalUnlock")
)

func read() (string, error) {
	r, _, err := openClipboard.Call(0)
	if r == 0 {
		return "", fmt.Errorf("OpenClipboard: %w", err)
	}
	defer closeClipboard.Call()

	h, _, err := getClipboardData.Call(cfUnicodeText)
	if h == 0 {
		return "", fmt.Errorf("GetClipboardData: %w", err)
	}

	ptr, _, err := globalLock.Call(h)
	if ptr == 0 {
		return "", fmt.Errorf("GlobalLock: %w", err)
	}
	defer globalUnlock.Call(h)

	return utf16PtrToString((*uint16)(unsafe.Pointer(ptr))), nil
}

// utf16PtrToString converts a null-terminated UTF-16 pointer to a Go string.
func utf16PtrToString(p *uint16) string {
	if p == nil {
		return ""
	}
	// Find null terminator.
	end := unsafe.Pointer(p)
	n := 0
	for *(*uint16)(end) != 0 {
		end = unsafe.Add(end, 2)
		n++
	}
	if n == 0 {
		return ""
	}
	u16s := unsafe.Slice(p, n)
	return syscall.UTF16ToString(u16s)
}
