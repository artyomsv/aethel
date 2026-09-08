//go:build windows

package userenv

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows/registry"
)

// HKCU\Environment is where Windows keeps the per-user environment — the same
// key the System Properties dialog and `setx` write.
const envKey = `Environment`

// `setx` is NOT used, and the reason is a real limit rather than taste: it
// truncates a value at 1024 characters, and an OAuth token is comfortably long
// enough to make that a live hazard — a truncated token is one that fails
// authentication with no indication that anything was cut.
func set(name, value string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, envKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open HKCU\\%s: %w", envKey, err)
	}
	defer k.Close()

	// SetStringValue writes REG_SZ. Deliberately not REG_EXPAND_SZ: a token is
	// an opaque credential, and a literal %NAME% inside one must reach the
	// process verbatim rather than being expanded away.
	if err := k.SetStringValue(name, value); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	broadcastEnvChange()
	return nil
}

func get(name string) (string, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, envKey, registry.QUERY_VALUE)
	if err != nil {
		return "", fmt.Errorf("open HKCU\\%s: %w", envKey, err)
	}
	defer k.Close()

	v, _, err := k.GetStringValue(name)
	if err == registry.ErrNotExist {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read %s: %w", name, err)
	}
	return v, nil
}

// broadcastEnvChange tells already-running programs to re-read the environment.
//
// Without it a registry write reaches only processes started from Explorer
// AFTER Explorer itself has noticed — so a terminal the user already has open
// keeps the old environment indefinitely, which reads as "quil said it worked
// and nothing happened". Best-effort: the caller's own immediate need is met
// by os.Setenv in the current process, so a failure here costs nothing but a
// reboot's worth of patience and is deliberately not reported.
//
// SendMessageTimeout, never SendMessage: the message goes to HWND_BROADCAST,
// so a single hung top-level window anywhere on the desktop would block this
// call forever.
func broadcastEnvChange() {
	user32 := syscall.NewLazyDLL("user32.dll")
	proc := user32.NewProc("SendMessageTimeoutW")

	const (
		hwndBroadcast   = 0xFFFF
		wmSettingChange = 0x001A
		smtoAbortIfHung = 0x0002
		timeoutMS       = 1000
	)
	env, err := syscall.UTF16PtrFromString("Environment")
	if err != nil {
		return
	}
	var result uintptr
	_, _, _ = proc.Call(
		uintptr(hwndBroadcast),
		uintptr(wmSettingChange),
		0,
		uintptr(unsafe.Pointer(env)),
		uintptr(smtoAbortIfHung),
		uintptr(timeoutMS),
		uintptr(unsafe.Pointer(&result)),
	)
}

// remove deletes a value. Only the Windows test uses it — the product never
// unsets the token, because a user who wants it gone edits it where the OS
// keeps it, the same place they would look for any other environment variable.
func remove(name string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, envKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.DeleteValue(name)
}
