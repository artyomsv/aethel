# Desktop Notifications Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Raise a native Windows toast when a pane parks for input or finishes a turn while the terminal is unfocused, and route Quil to that exact project/tab/pane when the toast is clicked.

**Architecture:** A new `internal/notify/` package splits platform-neutral logic (URI codec, toast XML, variant selection) from Windows-only syscalls (WinRT emit, `.lnk`+registry setup, named-pipe listener). The TUI owns the trigger — `blockedSince`/`unseen` are client-side `PaneModel` fields, so the daemon cannot compute it — and fires toasts from the existing before/after edge pair in `applyWorkTransition`. Clicking a toast launches `quil activate <uri>`, which writes a pane ID to a per-PID named pipe that the running TUI reads and turns into a `jumpToPane` call.

**Tech Stack:** Go 1.25, Bubble Tea v2 (`charm.land/bubbletea/v2`), `golang.org/x/sys/windows` (already a direct dependency — `windows/registry` and raw syscall interop need no new module), WinRT via `RoGetActivationFactory`.

**Spec:** [`docs/superpowers/specs/2026-08-13-desktop-notifications-design.md`](../specs/2026-08-13-desktop-notifications-design.md)

## Global Constraints

Every task's requirements implicitly include this section.

- **No intermediate commits.** This repo's standing rule: write to the working tree, do not commit per task. One clean commit at the end, only when the whole feature is complete and verified. This deliberately overrides the writing-plans skill's default per-task commit step — tasks end at **verify**, not at commit.
- **No AI attribution** in the eventual commit message: no `Co-Authored-By`, no model/vendor names, no "generated with" notes.
- **Production isolation.** Never touch `~/.quil/`, never run `kill-daemon`/`reset-daemon`, never run bare `./quil`. Test with `./quil-dev.exe` and confirm `[dev]` in the status bar.
- **Build/test commands:** `./scripts/dev.sh build`, `./scripts/dev.sh test ./internal/notify/`, `./scripts/dev.sh test ./internal/tui/`, `./scripts/dev.sh vet`. Go and make are NOT installed on the host; everything runs through Docker.
- **CI is Linux.** Anything behind `//go:build windows` is never compiled by `dev.sh test`. Keep all branching logic in neutral files. `GOOS=windows` vet proves only that it compiles.
- **Windows-native tests** run by building a test binary in Docker and executing it on the host:
  `docker run --rm -v "$PWD":/src -w /src -e GOOS=windows -e GOARCH=amd64 golang:1.25 go test -c -o /src/notify_win_test.exe ./internal/notify/` then `./notify_win_test.exe -test.v`. Delete the `.exe` afterwards; it is gitignored by `/quil*` only, so remove it by hand.
- **Go conventions:** tabs for indentation, `MixedCaps`, errors wrapped with `fmt.Errorf("...: %w", err)`, `//go:build` tags (not `// +build`), no `init()`, interfaces defined at the consumer.
- **Variant identifiers (exact values):**
  | | prod | dev |
  |---|---|---|
  | AUMID | `artyomsv.quil` | `artyomsv.quil.dev` |
  | Scheme | `quil` | `quil-dev` |
  | Shortcut | `Quil.lnk` | `Quil (dev).lnk` |
- **Pane ID format:** `pane-` followed by exactly 8 lowercase hex characters (matches `isValidHexID(id, "pane-")` at `internal/daemon/daemon.go:889`). Validate against this exact shape on both sides of the pipe.
- **Config defaults:** `enabled=true`, `blocked=true`, `done=true`, `cooldown="30s"`, `require_blur=true`.
- **One authority for config.** `raiseAttentionToast` reads `m.cfg.Notification.Desktop` directly. The Model keeps **no cached copy** — a second copy would make the Settings toggle a dual write, and it is reading `m.cfg` that makes the toggle apply live by construction.
- **Every Settings setter must set `m.configChanged = true`**, or the edit is silently discarded when the TUI exits.

## File Structure

| File | Responsibility |
|---|---|
| `internal/notify/notify.go` | `Notification`, `Notifier` interface, `Options`, `Variant()`, `New()` dispatch |
| `internal/notify/activate.go` | `quil://` URI build + parse. Neutral, pure |
| `internal/notify/toastxml.go` | Toast XML construction, escaping, bounding. Neutral, pure |
| `internal/notify/notify_windows.go` | WinRT `Notify`/`Withdraw`/`Close` |
| `internal/notify/notify_other.go` | No-op notifier for non-Windows |
| `internal/notify/setup_windows.go` | `.lnk` with AUMID, `HKCU` protocol key, `Status()` |
| `internal/notify/setup_other.go` | `ErrUnsupported` |
| `internal/notify/listener_windows.go` | `\\.\pipe\quil-activate-<pid>` server + client |
| `internal/notify/listener_other.go` | No-op listener |
| `internal/tui/notify.go` | `desktopNotifier` interface, `raiseAttentionToast`, `sweepOutstandingToasts`, gating |
| `internal/tui/workstate.go` | *(modify)* before/after capture + one emit call |
| `internal/tui/model.go` | *(modify)* Model fields, focus arms, `ReportFocus`, sweep call, `activatePaneMsg` arm |
| `internal/tui/pane.go` | *(modify)* `lastToastAt` field |
| `internal/tui/dialog.go` | *(modify)* one `settingsField` row, tri-state |
| `internal/config/config.go` | *(modify)* `DesktopConfig` + defaults |
| `cmd/quil/notify.go` | `quil notify setup\|--remove\|status`, `quil activate <uri>` |
| `cmd/quil/main.go` | *(modify)* subcommand dispatch, notifier + listener wiring |

Tasks 1–8 are fully testable in Linux CI and deliver a complete, tested feature that is inert (no-op notifier). Tasks 9–12 add the Windows surface.

---

### Task 1: Spike — confirm pure-Go WinRT toast emission

**This is a spike. Its output is an answer, not code you keep.** The spec commits to direct WinRT interop over shelling out to PowerShell; this task proves that is reasonable before eleven tasks depend on it.

**Files:**
- Create (throwaway): `scratch/winrt_spike/main.go` — delete when done

**Interfaces:**
- Consumes: nothing
- Produces: a go/no-go answer recorded in this plan file, plus the confirmed IID constants that Task 9 will use

- [ ] **Step 1: Write a throwaway program that shows one toast**

Create `scratch/winrt_spike/main.go`. The call sequence to prove out:

```go
//go:build windows

package main

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Minimum viable sequence:
//   1. RoInitialize(RO_INIT_MULTITHREADED = 1)
//   2. WindowsCreateString("Windows.Data.Xml.Dom.XmlDocument") -> HSTRING
//      RoActivateInstance -> IXmlDocument; call LoadXml(toastXML)
//   3. RoGetActivationFactory("Windows.UI.Notifications.ToastNotification",
//        IID_IToastNotificationFactory) -> factory
//      factory.CreateToastNotification(xmlDoc) -> IToastNotification
//   4. RoGetActivationFactory("Windows.UI.Notifications.ToastNotificationManager",
//        IID_IToastNotificationManagerStatics) -> statics
//      statics.CreateToastNotifierWithId(HSTRING(aumid)) -> IToastNotifier
//   5. notifier.Show(toast)
//
// Withdraw sequence to prove separately:
//   RoGetActivationFactory(..., IID_IToastNotificationManagerStatics2) -> statics2
//   statics2.get_History() -> IToastNotificationHistory
//   history.RemoveGroupedTagWithId(tag, group, aumid)

func main() {
	fmt.Println("spike: see steps above")
	_ = windows.CoInitializeEx
	_ = syscall.NewLazyDLL
	_ = unsafe.Pointer(nil)
}
```

Prerequisite: the AUMID must already exist. Create the Start Menu shortcut by hand for the spike only — run this in PowerShell:

```powershell
$s = (New-Object -ComObject WScript.Shell).CreateShortcut("$env:APPDATA\Microsoft\Windows\Start Menu\Programs\QuilSpike.lnk")
$s.TargetPath = "$PWD\quil-dev.exe"
$s.Save()
```

Then set its AUMID property. If setting the property from PowerShell proves hard, that itself is a finding for Task 10 — record it.

- [ ] **Step 2: Build and run it on the host**

```bash
docker run --rm -v "$PWD":/src -w /src -e GOOS=windows -e GOARCH=amd64 golang:1.25 \
  go build -o /src/winrt_spike.exe ./scratch/winrt_spike/
./winrt_spike.exe
```

Expected: a Windows toast appears.

#### SPIKE RESULT: **GO** (run 2026-08-13, Windows 10 Pro 19045)

Pure-Go COM/WinRT interop works for all three requirements. No CGo, no
PowerShell, no new dependency — `golang.org/x/sys/windows` plus
`syscall.SyscallN` over raw vtables. The interop layer is ~250 lines.

Verified output:

```
ok    RoInitialize
ok    createShortcut (…\Start Menu\Programs\QuilSpike.lnk)
      tag="pane-00000000" group="artyomsv.quil.spike" (read back)
      notifier setting = 0 (Enabled)
ok    showToast
      idx8(tag,group,appId) -> S_OK
ok    withdrawToast
```

**Confirmed GUIDs**

| Symbol | Value |
|---|---|
| `CLSID_ShellLink` | `00021401-0000-0000-C000-000000000046` |
| `IID_IShellLinkW` | `000214F9-0000-0000-C000-000000000046` |
| `IID_IPersistFile` | `0000010B-0000-0000-C000-000000000046` |
| `IID_IPropertyStore` | `886D8EEB-8CF2-4446-8D02-CDBA1DBDCF99` |
| `PKEY_AppUserModel_ID` | fmtid `9F4C2855-9F79-4B39-A8D0-E1D42DE1D5F3`, pid `5` |
| `IID_IToastNotificationManagerStatics` | `50AC103F-D235-4598-BBEF-98FE4D1A3AD4` |
| `IID_IToastNotificationManagerStatics2` | `7AB93C52-0E48-4750-BA9D-1A4113981847` |
| `IID_IToastNotificationFactory` | `04124B20-82C6-4229-B109-FD9ED4662B53` |
| `IID_IXmlDocumentIO` | `6CD0E74E-EE65-4489-9EBF-CA43E87BA637` |
| `IID_IToastNotification2` | `9DFB9FD1-143A-490E-90BF-B9FBA7132DE7` |

**Confirmed vtable indices** (IInspectable occupies 3–5, so members start at 6)

| Interface | Members |
|---|---|
| `IShellLinkW` | `SetIconLocation` 17, `SetPath` 20 |
| `IPropertyStore` | `SetValue` 6, `Commit` 7 |
| `IPersistFile` | `Save` 6 |
| `IXmlDocumentIO` | `LoadXml` 6 |
| `IToastNotificationFactory` | `CreateToastNotification` 6 |
| `IToastNotificationManagerStatics` | `CreateToastNotifierWithId` 7 |
| `IToastNotificationManagerStatics2` | `get_History` 6 |
| `IToastNotifier` | `Show` 6, `Hide` 7, `get_Setting` 8 |
| `IToastNotification2` | `put_Tag` 6, `get_Tag` 7, `put_Group` 8, `get_Group` 9 |
| `IToastNotificationHistory` | `RemoveGroupedTagWithId` **8** |

**Three findings Task 9 must carry over.**

1. **`IToastNotification2` declares `put_` BEFORE `get_`.** Using 7/9 as the
   setters returns `S_OK` and sets nothing — they are the getters, and an
   `HSTRING` passed where an `HSTRING*` out-param is expected is accepted
   silently. The only symptom is that withdraw later reports `ERROR_NOT_FOUND`.
   **Task 9 must read the tag back after setting it**, exactly as the spike
   does; a setter at a wrong index has no other observable failure.
2. **Never `PropVariantClear` a `PROPVARIANT` built over Go memory.** It
   `CoTaskMemFree`s the inner pointer, and ours is a Go-allocated UTF-16
   buffer. `SetValue` copies into the store, so there is nothing to release.
   (It also lives in `ole32.dll`, not `oleaut32.dll`.)
3. **`IToastNotifier::get_Setting` is the only way to tell "displayed" from
   "accepted and dropped."** `Show` returns `S_OK` for an unregistered AUMID
   too. Task 9 should surface a non-zero setting as an error rather than
   reporting success — `4` means the AUMID is not recognised.

Not needed: `RoActivateInstance` for `XmlDocument` worked without a manifest,
and no shell reindex or logoff was required — the shortcut was honoured
immediately after `IPersistFile::Save`.

- [ ] ~~**Step 3: Record the answer in this file**~~ — done, see above

Append a short block under this task: go / no-go, the exact IID GUIDs that worked, the approximate line count of the interop layer, and whether `RemoveGroupedTagWithId` worked. If pure-Go interop proves unreasonable, **stop and escalate** — do not silently fall back to PowerShell; the spec rejects it on latency and console-flash grounds, and that rejection needs re-litigating with the user rather than working around.

- [ ] **Step 4: Delete the throwaway**

```bash
rm -rf scratch/winrt_spike winrt_spike.exe
```

Also delete the `QuilSpike.lnk` shortcut. Verify `git status --short` shows no stray files.

---

### Task 2: `quil://` URI codec

**Files:**
- Create: `internal/notify/activate.go`
- Test: `internal/notify/activate_test.go`

**Interfaces:**
- Consumes: nothing
- Produces:
  - `func BuildActivateURI(scheme string, pid int, paneID string) string`
  - `func ParseActivateURI(scheme, raw string) (pid int, paneID string, err error)`
  - `func ValidPaneID(id string) bool`
  - `var ErrBadURI = errors.New("notify: malformed activation URI")`

- [ ] **Step 1: Write the failing test**

Create `internal/notify/activate_test.go`:

```go
package notify

import (
	"strings"
	"testing"
)

func TestActivateURI_RoundTrip(t *testing.T) {
	t.Parallel()
	got := BuildActivateURI("quil", 4321, "pane-0a1b2c3d")
	pid, paneID, err := ParseActivateURI("quil", got)
	if err != nil {
		t.Fatalf("ParseActivateURI(%q) error = %v", got, err)
	}
	if pid != 4321 {
		t.Errorf("pid = %d, want 4321", pid)
	}
	if paneID != "pane-0a1b2c3d" {
		t.Errorf("paneID = %q, want pane-0a1b2c3d", paneID)
	}
}

func TestParseActivateURI_Rejects(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		scheme string
		raw    string
	}{
		{"empty", "quil", ""},
		{"wrong scheme", "quil", "quil-dev://activate?pid=1&pane=pane-0a1b2c3d"},
		{"dev scheme against prod", "quil-dev", "quil://activate?pid=1&pane=pane-0a1b2c3d"},
		{"no pane", "quil", "quil://activate?pid=1"},
		{"no pid", "quil", "quil://activate?pane=pane-0a1b2c3d"},
		{"pid not a number", "quil", "quil://activate?pid=abc&pane=pane-0a1b2c3d"},
		{"pid negative", "quil", "quil://activate?pid=-5&pane=pane-0a1b2c3d"},
		{"pid zero", "quil", "quil://activate?pid=0&pane=pane-0a1b2c3d"},
		{"pane wrong prefix", "quil", "quil://activate?pid=1&pane=tab-0a1b2c3d"},
		{"pane too short", "quil", "quil://activate?pid=1&pane=pane-0a1b2c3"},
		{"pane too long", "quil", "quil://activate?pid=1&pane=pane-0a1b2c3d4"},
		{"pane uppercase hex", "quil", "quil://activate?pid=1&pane=pane-0A1B2C3D"},
		{"pane non-hex", "quil", "quil://activate?pid=1&pane=pane-0a1b2c3g"},
		{"path traversal", "quil", "quil://activate?pid=1&pane=..%2f..%2fetc"},
		{"wrong host", "quil", "quil://spawn?pid=1&pane=pane-0a1b2c3d"},
		{"not a uri", "quil", "hello world"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, _, err := ParseActivateURI(tt.scheme, tt.raw); err == nil {
				t.Errorf("ParseActivateURI(%q, %q) = nil error, want rejection", tt.scheme, tt.raw)
			}
		})
	}
}

// The URI is registry-reachable by any local process, so a hostile value must
// not survive parsing. This asserts the ceiling the design depends on: the only
// thing that can come out is a well-formed pane ID.
func TestParseActivateURI_OnlyEmitsWellFormedPaneIDs(t *testing.T) {
	t.Parallel()
	hostile := []string{
		"quil://activate?pid=1&pane=pane-0a1b2c3d%00evil",
		"quil://activate?pid=1&pane=" + strings.Repeat("a", 5000),
		"quil://activate?pid=1&pane=pane-0a1b2c3d&pane=pane-11111111",
	}
	for _, raw := range hostile {
		_, paneID, err := ParseActivateURI("quil", raw)
		if err == nil && !ValidPaneID(paneID) {
			t.Errorf("ParseActivateURI(%q) accepted invalid pane %q", raw, paneID)
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `./scripts/dev.sh test ./internal/notify/`
Expected: FAIL — build error, `undefined: BuildActivateURI`.

- [ ] **Step 3: Write the implementation**

Create `internal/notify/activate.go`:

```go
// Package notify raises operating-system notifications for pane attention
// events and routes their activation back into the running TUI.
//
// The package is split so that every file carrying logic is platform-neutral
// and every platform-specific file is close to logic-free. CI builds only
// Linux, so a branch behind //go:build windows is never exercised by
// `dev.sh test` — the same gap that let a statically-dead assignment in
// browse_roots.go pass every test in the tree.
package notify

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
)

// ErrBadURI rejects anything that is not a well-formed activation URI for the
// expected scheme.
var ErrBadURI = errors.New("notify: malformed activation URI")

// activateHost is the single URI host this package will act on. Parsing is
// pinned to it so that adding a second verb later is a deliberate act rather
// than something a caller can reach by guessing.
const activateHost = "activate"

// BuildActivateURI renders the toast's launch target.
//
// The PID is what makes activation address the client that RAISED the toast
// rather than every client attached to the daemon. Several TUIs can be running
// against one workspace, and the toast belongs to exactly one of them.
func BuildActivateURI(scheme string, pid int, paneID string) string {
	q := url.Values{}
	q.Set("pid", strconv.Itoa(pid))
	q.Set("pane", paneID)
	return scheme + "://" + activateHost + "?" + q.Encode()
}

// ParseActivateURI validates a URI handed to us by the operating system.
//
// Registering a URI scheme makes this reachable by ANY local process, and by a
// web page behind a browser confirmation — so this is a trust boundary, not a
// formality. Everything it can produce is a PID and a pane ID that already
// passed ValidPaneID; there is deliberately no path from here to a command, an
// argument list or a filesystem path.
func ParseActivateURI(scheme, raw string) (pid int, paneID string, err error) {
	u, err := url.Parse(raw)
	if err != nil {
		return 0, "", fmt.Errorf("%w: %v", ErrBadURI, err)
	}
	if u.Scheme != scheme {
		return 0, "", fmt.Errorf("%w: scheme %q, want %q", ErrBadURI, u.Scheme, scheme)
	}
	if u.Host != activateHost {
		return 0, "", fmt.Errorf("%w: host %q, want %q", ErrBadURI, u.Host, activateHost)
	}
	q := u.Query()
	// Repeated keys are refused rather than resolved by taking the first or
	// last: a caller sending pane= twice is not a caller we are willing to
	// guess for, and url.Values silently keeps both.
	if len(q["pid"]) != 1 || len(q["pane"]) != 1 {
		return 0, "", fmt.Errorf("%w: pid and pane must each appear exactly once", ErrBadURI)
	}
	pid, err = strconv.Atoi(q.Get("pid"))
	if err != nil || pid <= 0 {
		return 0, "", fmt.Errorf("%w: bad pid %q", ErrBadURI, q.Get("pid"))
	}
	paneID = q.Get("pane")
	if !ValidPaneID(paneID) {
		return 0, "", fmt.Errorf("%w: bad pane id", ErrBadURI)
	}
	return pid, paneID, nil
}

// ValidPaneID enforces the daemon's own pane-id shape: "pane-" plus exactly
// eight lowercase hex digits (see isValidHexID, internal/daemon/daemon.go).
//
// Deliberately the STRICT format rather than hookevents.safePaneID's weaker
// "cannot escape a filepath.Join" rule. That rule exists to protect a path
// join; this one is the whole guarantee that a registry-reachable URI cannot
// name anything but a pane.
func ValidPaneID(id string) bool {
	const prefix = "pane-"
	const hexLen = 8
	if len(id) != len(prefix)+hexLen {
		return false
	}
	if id[:len(prefix)] != prefix {
		return false
	}
	for i := len(prefix); i < len(id); i++ {
		c := id[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `./scripts/dev.sh test ./internal/notify/`
Expected: PASS, all subtests.

- [ ] **Step 5: Verify (no commit — see Global Constraints)**

Run: `./scripts/dev.sh vet`
Expected: clean.

---

### Task 3: Toast XML builder

**Files:**
- Create: `internal/notify/toastxml.go`
- Test: `internal/notify/toastxml_test.go`

**Interfaces:**
- Consumes: `BuildActivateURI` (Task 2) — only in callers, not here
- Produces:
  - `type Notification struct { Title, Body, Tag, Launch string }`
  - `func BuildToastXML(n Notification) string`
  - `const MaxFieldRunes = 120`

**Note — a refinement of the spec.** The spec places sanitize + escape + bound together in `toastxml.go`. `sanitizeRemoteText` is unexported in `internal/tui` and the repo's own rule is that sanitizing happens **at render, never in state** — the toast is one more render surface, so sanitizing belongs at the `Notification` build site in `internal/tui` (Task 7), not here. This task owns escaping and bounding. Both halves stay Linux-testable, which is what the spec's placement was protecting.

- [ ] **Step 1: Write the failing test**

Create `internal/notify/toastxml_test.go`:

```go
package notify

import (
	"strings"
	"testing"
)

func TestBuildToastXML_ContainsFields(t *testing.T) {
	t.Parallel()
	xml := BuildToastXML(Notification{
		Title:  "quil · claude-code",
		Body:   "▲ Waiting for your input",
		Tag:    "pane-0a1b2c3d",
		Launch: "quil://activate?pane=pane-0a1b2c3d&pid=1",
	})
	for _, want := range []string{
		`activationType="protocol"`,
		`launch="quil://activate?pane=pane-0a1b2c3d&amp;pid=1"`,
		"quil · claude-code",
		"▲ Waiting for your input",
	} {
		if !strings.Contains(xml, want) {
			t.Errorf("BuildToastXML() missing %q\ngot:\n%s", want, xml)
		}
	}
}

func TestBuildToastXML_EscapesXMLMetacharacters(t *testing.T) {
	t.Parallel()
	xml := BuildToastXML(Notification{
		Title: `a & b <script> "q"`,
		Body:  `x > y & z`,
	})
	if strings.Contains(xml, "<script>") {
		t.Errorf("unescaped element leaked into XML:\n%s", xml)
	}
	if !strings.Contains(xml, "&amp;") {
		t.Errorf("ampersand not escaped:\n%s", xml)
	}
}

// A project or pane name arrives from a daemon the user may not control.
// Bidi overrides are PRINTABLE, so XML escaping passes them through untouched
// and the toast renders a reversed pane name. This is the OSC 52 defect shape
// in a new surface: an escaping pass is not a sanitising pass.
func TestBuildToastXML_StripsBidiAndControls(t *testing.T) {
	t.Parallel()
	xml := BuildToastXML(Notification{
		Title: "safe\u202Ereversed",
		Body:  "before\u009Bafter\x07\x00end",
	})
	for _, bad := range []string{"\u202E", "\u009B", "\x07", "\x00"} {
		if strings.Contains(xml, bad) {
			t.Errorf("BuildToastXML() leaked %q\ngot:\n%s", bad, xml)
		}
	}
	if !strings.Contains(xml, "safe") || !strings.Contains(xml, "reversed") {
		t.Errorf("printable text was destroyed:\n%s", xml)
	}
}

// Sanitising does not shorten anything — a megabyte of ordinary printable text
// survives it whole — so bounding is a third, separate concern.
func TestBuildToastXML_BoundsFieldLength(t *testing.T) {
	t.Parallel()
	xml := BuildToastXML(Notification{
		Title: strings.Repeat("a", 10_000),
		Body:  strings.Repeat("b", 10_000),
	})
	if len(xml) > 4096 {
		t.Errorf("toast XML = %d bytes, want bounded well under 4096", len(xml))
	}
}

func TestBuildToastXML_OmitsLaunchWhenEmpty(t *testing.T) {
	t.Parallel()
	xml := BuildToastXML(Notification{Title: "t", Body: "b"})
	if strings.Contains(xml, "activationType") {
		t.Errorf("launch attributes emitted for a toast with no launch URI:\n%s", xml)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `./scripts/dev.sh test ./internal/notify/`
Expected: FAIL — `undefined: BuildToastXML`.

- [ ] **Step 3: Write the implementation**

Create `internal/notify/toastxml.go`:

```go
package notify

import (
	"encoding/xml"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxFieldRunes bounds each rendered field.
//
// Separate from sanitising on purpose: a control-character filter preserves
// printable non-ASCII byte-identically, so it removes escapes without
// shortening anything. An unbounded project name would otherwise produce a
// toast payload Windows truncates or rejects on its own terms, which is a
// failure the user sees and we cannot explain.
const MaxFieldRunes = 120

// Notification is one toast. Title and Body are expected to have already been
// through the caller's remote-text sanitiser (internal/tui does this at the
// build site, matching its render-only rule); this package escapes and bounds
// them, and drops anything that survived.
type Notification struct {
	Title  string
	Body   string
	Tag    string // per-pane key, used to withdraw the toast later
	Launch string // activation URI; empty means a toast with no click target
}

// BuildToastXML renders the ToastGeneric payload.
//
// activationType="protocol" is what lets the click reach a registered URI
// handler with no COM activator — the alternative needs a CLSID and a
// hand-implemented INotificationActivationCallback vtable, with no CGo
// available.
func BuildToastXML(n Notification) string {
	title := clean(n.Title)
	body := clean(n.Body)

	var b strings.Builder
	b.WriteString(`<toast`)
	if n.Launch != "" {
		b.WriteString(` activationType="protocol" launch="`)
		b.WriteString(escape(n.Launch))
		b.WriteString(`"`)
	}
	b.WriteString(`><visual><binding template="ToastGeneric">`)
	b.WriteString(`<text>`)
	b.WriteString(escape(title))
	b.WriteString(`</text><text>`)
	b.WriteString(escape(body))
	b.WriteString(`</text></binding></visual></toast>`)
	return b.String()
}

// clean drops what must never reach a rendered surface, then bounds what is
// left. Order matters: bounding first could cut a multi-byte sequence and
// leave a replacement character behind for the filter to keep.
func clean(s string) string {
	s = sanitize(s)
	return truncateRunes(s, MaxFieldRunes)
}

// sanitize removes C0/DEL, the C1 range (including U+009B, the CSI introducer
// internal/tui/oscfilter.go exists because of), and the bidi overrides and
// isolates. Tab becomes a space so a rendered width matches a measured one.
//
// Printable non-ASCII is preserved byte-identically — this is a control
// filter, not a transliteration.
func sanitize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\t', r == '\n', r == '\r':
			b.WriteRune(' ')
		case r < 0x20 || r == 0x7f:
			// C0 and DEL
		case r >= 0x80 && r <= 0x9f:
			// C1, including U+009B
		case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
			// Bidi overrides and isolates. These are PRINTABLE, so no
			// control-character test catches them and no XML escape touches
			// them — U+202E alone reverses the rest of the rendered line.
		case r == utf8.RuneError:
			// Invalid UTF-8 that range already replaced.
		case !unicode.IsPrint(r) && r != ' ':
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func truncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	n := 0
	for i := range s {
		if n == max {
			return s[:i] + "…"
		}
		n++
	}
	return s
}

// escape uses encoding/xml so the escaping rules are the standard library's
// rather than a hand-rolled replacer that forgets a case.
func escape(s string) string {
	var b strings.Builder
	if err := xml.EscapeText(&b, []byte(s)); err != nil {
		return ""
	}
	return b.String()
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `./scripts/dev.sh test ./internal/notify/`
Expected: PASS.

- [ ] **Step 5: Verify**

Run: `./scripts/dev.sh vet`
Expected: clean.

---

### Task 4: Notifier interface, variant selection, no-op implementation

**Files:**
- Create: `internal/notify/notify.go`
- Create: `internal/notify/notify_other.go`
- Test: `internal/notify/notify_test.go`

**Interfaces:**
- Consumes: `Notification` (Task 3)
- Produces:
  - `type Notifier interface { Notify(Notification) error; Withdraw(tag string) error; Close() error }`
  - `type Options struct { AUMID, Scheme string }`
  - `func Variant(dev bool) Options`
  - `func New(opts Options) (Notifier, error)`
  - `var ErrUnsupported = errors.New("notify: not supported on this platform")`
  - `var ErrNotRegistered = errors.New("notify: run 'quil notify setup' first")`

- [ ] **Step 1: Write the failing test**

Create `internal/notify/notify_test.go`:

```go
package notify

import "testing"

// The dev build writes machine-global artifacts that QUIL_HOME cannot
// redirect — a Start Menu shortcut and an HKCU class key. Namespacing them is
// the only thing keeping a dev build from overwriting production's
// registration and pointing quil:// at the dev binary, which
// .claude/rules/dev-environment.md forbids.
func TestVariant_DevAndProdAreDisjoint(t *testing.T) {
	t.Parallel()
	prod := Variant(false)
	dev := Variant(true)

	if prod.AUMID != "artyomsv.quil" {
		t.Errorf("prod AUMID = %q, want artyomsv.quil", prod.AUMID)
	}
	if prod.Scheme != "quil" {
		t.Errorf("prod Scheme = %q, want quil", prod.Scheme)
	}
	if dev.AUMID != "artyomsv.quil.dev" {
		t.Errorf("dev AUMID = %q, want artyomsv.quil.dev", dev.AUMID)
	}
	if dev.Scheme != "quil-dev" {
		t.Errorf("dev Scheme = %q, want quil-dev", dev.Scheme)
	}
	if prod.AUMID == dev.AUMID || prod.Scheme == dev.Scheme {
		t.Fatal("dev and prod variants must not share an AUMID or a scheme")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `./scripts/dev.sh test ./internal/notify/`
Expected: FAIL — `undefined: Variant`.

- [ ] **Step 3: Write the implementation**

Create `internal/notify/notify.go`:

```go
package notify

import "errors"

var (
	// ErrUnsupported is returned by the setup commands on a platform with no
	// toast support. New() does NOT return it — it returns a nil Notifier, so
	// callers never branch on platform.
	ErrUnsupported = errors.New("notify: not supported on this platform")

	// ErrNotRegistered means the AUMID has no backing Start Menu shortcut, so
	// Windows will refuse to display a toast.
	ErrNotRegistered = errors.New("notify: run 'quil notify setup' first")
)

// Notifier raises and withdraws operating-system notifications.
//
// Withdraw is on the interface from the start rather than added later:
// Windows toasts persist in Action Center indefinitely, so without it,
// answering a prompt leaves a toast still claiming the pane needs attention.
type Notifier interface {
	Notify(Notification) error
	Withdraw(tag string) error
	Close() error
}

// Options identifies which registration a process should use.
type Options struct {
	AUMID  string
	Scheme string
}

// Variant returns the identifiers for a build.
//
// These artifacts are machine-global — a Start Menu shortcut and an HKCU class
// key — which makes them the first thing Quil writes that QUIL_HOME cannot
// redirect. Namespacing them by build variant is what lets a dev instance
// coexist with production the way the daemons already do, and is also what
// makes the feature testable in dev mode at all.
func Variant(dev bool) Options {
	if dev {
		return Options{AUMID: "artyomsv.quil.dev", Scheme: "quil-dev"}
	}
	return Options{AUMID: "artyomsv.quil", Scheme: "quil"}
}
```

Create `internal/notify/notify_other.go`:

```go
//go:build !windows

package notify

// New returns a nil Notifier everywhere but Windows.
//
// Nil rather than a no-op struct, and nil rather than an error: the caller
// stores it in an interface field and checks that field once, so no call site
// carries a platform branch. Desktop toasts on macOS and Linux are separate,
// lesser work — no transport there supports click routing, which is this
// feature's point.
func New(opts Options) (Notifier, error) { return nil, nil }
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `./scripts/dev.sh test ./internal/notify/`
Expected: PASS.

- [ ] **Step 5: Verify**

Run: `./scripts/dev.sh vet`
Expected: clean.

---

### Task 5: Config schema

**Files:**
- Modify: `internal/config/config.go:111-115` (`NotificationConfig`) and the defaults block near line 341
- Test: `internal/config/config_test.go`

**Interfaces:**
- Consumes: nothing
- Produces: `config.DesktopConfig` with fields `Enabled bool`, `Blocked bool`, `Done bool`, `Cooldown string`, `RequireBlur bool`, and method `func (d DesktopConfig) CooldownDuration() time.Duration`

- [ ] **Step 1: Write the failing test**

Append to `internal/config/config_test.go`:

```go
func TestDesktopNotificationDefaults(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	cfg := config.Default()
	d := cfg.Notification.Desktop

	if !d.Enabled {
		t.Error("desktop notifications must default to ON — the flag expresses intent; registration is the gate")
	}
	if !d.Blocked || !d.Done {
		t.Errorf("both kinds default on once enabled: blocked=%v done=%v", d.Blocked, d.Done)
	}
	if !d.RequireBlur {
		t.Error("require_blur must default true")
	}
	if got := d.CooldownDuration(); got != 30*time.Second {
		t.Errorf("CooldownDuration() = %v, want 30s", got)
	}
}

func TestDesktopCooldownDuration_FallsBackOnGarbage(t *testing.T) {
	tests := []struct {
		in   string
		want time.Duration
	}{
		{"30s", 30 * time.Second},
		{"2m", 2 * time.Minute},
		{"", 30 * time.Second},
		{"nonsense", 30 * time.Second},
		{"-5s", 30 * time.Second},
	}
	for _, tt := range tests {
		d := config.DesktopConfig{Cooldown: tt.in}
		if got := d.CooldownDuration(); got != tt.want {
			t.Errorf("Cooldown %q → %v, want %v", tt.in, got, tt.want)
		}
	}
}
```

Add `"time"` to that file's imports if absent.

- [ ] **Step 2: Run the test to verify it fails**

Run: `./scripts/dev.sh test ./internal/config/`
Expected: FAIL — `d.Enabled undefined`.

- [ ] **Step 3: Write the implementation**

In `internal/config/config.go`, replace `NotificationConfig`:

```go
type NotificationConfig struct {
	SidebarWidth int                     `toml:"sidebar_width"` // default 30
	MaxEvents    int                     `toml:"max_events"`    // default 200
	Hooks        HookNotificationsConfig `toml:"hooks"`
	Desktop      DesktopConfig           `toml:"desktop"`
}

// DesktopConfig controls operating-system toasts raised from the project
// sidebar's attention model.
//
// Enabled defaults TRUE, and that does not make registration implicit: Windows
// toasts need a Start Menu shortcut and an HKCU class key, both outside
// QUIL_HOME, and nothing writes them as a side effect of a config flag. The
// flag says "I want these"; `quil notify setup` is still the gate.
//
// The consequence is that enabled-but-unregistered is the DEFAULT state on a
// fresh Windows install rather than an edge case, which is why the Settings
// row reports registration state instead of this bool.
type DesktopConfig struct {
	Enabled bool `toml:"enabled"`
	Blocked bool `toml:"blocked"` // ▲ a pane parked waiting on the user
	Done    bool `toml:"done"`    // ✓ a turn finished while the user was away

	// Cooldown is per PANE and shared by both kinds. Shared deliberately: a
	// pane that blocks and then completes five seconds later should produce
	// one toast, not two.
	Cooldown string `toml:"cooldown"`

	// RequireBlur suppresses toasts while the terminal has focus. Turning it
	// off is the escape hatch for a terminal that does not implement focus
	// reporting (DEC 1004), where no focus event ever arrives and the gate
	// would otherwise suppress everything forever.
	RequireBlur bool `toml:"require_blur"`
}

// DefaultDesktopCooldown matches the per-pane bell/idle convention the daemon
// already uses (Pane.LastBellEventAt).
const DefaultDesktopCooldown = 30 * time.Second

// CooldownDuration parses Cooldown, falling back to the default for an empty,
// malformed or non-positive value. A garbage value must not disable the
// rate limit — that would turn a typo into a toast storm.
func (d DesktopConfig) CooldownDuration() time.Duration {
	v, err := time.ParseDuration(d.Cooldown)
	if err != nil || v <= 0 {
		return DefaultDesktopCooldown
	}
	return v
}
```

In the defaults block near line 341:

```go
		Notification: NotificationConfig{
			SidebarWidth: 30,
			MaxEvents:    200,
			Desktop: DesktopConfig{
				Enabled:     true,
				Blocked:     true,
				Done:        true,
				Cooldown:    "30s",
				RequireBlur: true,
			},
		},
```

Ensure `"time"` is imported in `config.go`.

- [ ] **Step 4: Run the test to verify it passes**

Run: `./scripts/dev.sh test ./internal/config/`
Expected: PASS.

- [ ] **Step 5: Verify**

Run: `./scripts/dev.sh test` and `./scripts/dev.sh vet`
Expected: whole suite clean — `Default()` is read by many tests.

---

### Task 6: Terminal focus reporting

**Files:**
- Modify: `internal/tui/model.go` — Model fields, `View()` near line 3490, `Update` message arms
- Test: `internal/tui/focus_test.go`

**Interfaces:**
- Consumes: nothing
- Produces: `Model.termFocused bool`, `Model.focusEverReported bool`, `func (m Model) ShouldToastNow() bool` *(exported for tests in the same package is unnecessary — keep it unexported as `shouldToastNow`)*

- [ ] **Step 1: Write the failing test**

Create `internal/tui/focus_test.go`:

```go
package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestFocusMsg_TracksTerminalFocus(t *testing.T) {
	m := Model{termFocused: true}

	updated, _ := m.Update(tea.BlurMsg{})
	m = updated.(Model)
	if m.termFocused {
		t.Error("BlurMsg must clear termFocused")
	}
	if !m.focusEverReported {
		t.Error("BlurMsg must record that focus reporting works")
	}

	updated, _ = m.Update(tea.FocusMsg{})
	m = updated.(Model)
	if !m.termFocused {
		t.Error("FocusMsg must set termFocused")
	}
}

// A terminal without DEC 1004 support never sends either message, so
// termFocused keeps its initial value forever. Initialising it TRUE means the
// feature silently does nothing there rather than storming the user with
// toasts while they look straight at the screen. focusEverReported is what
// makes that state diagnosable instead of mysterious.
func TestNewModel_AssumesFocusedUntilProvenOtherwise(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	m := newTestModel(t)
	if !m.termFocused {
		t.Error("termFocused must start true so an unsupporting terminal fails quiet, not loud")
	}
	if m.focusEverReported {
		t.Error("focusEverReported must start false")
	}
}

func TestView_EnablesFocusReporting(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	m := newTestModel(t)
	m.width, m.height = 80, 24
	if !m.View().ReportFocus {
		t.Error("View must set ReportFocus or no focus event ever arrives")
	}
}
```

If no `newTestModel` helper exists in the package, add one to this file that builds a Model the same way the existing tests in `internal/tui` do — read a neighbouring `_test.go` file first and copy its construction, rather than inventing a second way to build a Model.

- [ ] **Step 2: Run the test to verify it fails**

Run: `./scripts/dev.sh test ./internal/tui/`
Expected: FAIL — `m.termFocused undefined`.

- [ ] **Step 3: Write the implementation**

Add to the `Model` struct in `internal/tui/model.go`:

```go
	// termFocused tracks whether the terminal window has focus, from
	// tea.FocusMsg/BlurMsg (DEC 1004).
	//
	// Initialised TRUE. A terminal that does not implement focus reporting
	// sends neither message, so this keeps its initial value for the life of
	// the process — and the two possible defaults fail in opposite
	// directions. True means desktop toasts silently never fire there; false
	// means they fire constantly while the user is looking at the screen.
	// Quiet-and-diagnosable beats loud-and-wrong, so this starts true and
	// focusEverReported exists to explain the silence.
	termFocused bool

	// focusEverReported records that at least one focus event arrived, i.e.
	// that the terminal really does implement DEC 1004. `quil notify status`
	// and the startup warning read it; without it, "no toasts ever appear" has
	// no distinguishable cause.
	focusEverReported bool
```

In `NewModel` (near line 788), set it:

```go
	m.termFocused = true
```

In `Update`, add arms beside the other message types:

```go
	case tea.FocusMsg:
		m.termFocused = true
		m.focusEverReported = true
		return m, nil

	case tea.BlurMsg:
		m.termFocused = false
		m.focusEverReported = true
		return m, nil
```

In `View()` beside `v.AltScreen = true` (line 3491):

```go
	v.ReportFocus = true
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `./scripts/dev.sh test ./internal/tui/`
Expected: PASS.

- [ ] **Step 5: Verify no regression**

Run: `./scripts/dev.sh test`
Expected: whole suite passes. `ReportFocus` adds an escape sequence to the terminal setup; if any snapshot test compares raw terminal output, it may need updating — read the failure before changing an expectation.

---

### Task 7: Attention edge detection, gating, and cooldown

This is the core of the feature and the largest task. It ends with a fully tested trigger that drives a fake notifier.

**Files:**
- Create: `internal/tui/notify.go`
- Create: `internal/tui/notify_test.go`
- Modify: `internal/tui/workstate.go:233-424` (`applyWorkTransition`)
- Modify: `internal/tui/pane.go` (`PaneModel` — add `lastToastAt`)
- Modify: `internal/tui/model.go` (Model fields + `SetDesktopNotifier`)

**Interfaces:**
- Consumes: `notify.Notification` (Task 3), `notify.BuildActivateURI` (Task 2), `m.cfg.Notification.Desktop` (Task 5 — read directly, never cached), `Model.termFocused` (Task 6)
- Produces:
  - `type desktopNotifier interface { Notify(notify.Notification) error; Withdraw(tag string) error }`
  - `func (m *Model) SetDesktopNotifier(n desktopNotifier, scheme string, pid int)`
  - `func (m *Model) raiseAttentionToast(pane *PaneModel, proj *ProjectModel, wasBlocked, wasUnseen bool)`
  - `PaneModel.lastToastAt time.Time`
  - `Model.outstandingToasts map[string]struct{}`

- [ ] **Step 1: Write the failing test**

Create `internal/tui/notify_test.go`:

```go
package tui

import (
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/notify"
)

// fakeNotifier records calls so the whole trigger path is testable on Linux,
// where no real toast mechanism exists.
type fakeNotifier struct {
	sent      []notify.Notification
	withdrawn []string
	err       error
}

func (f *fakeNotifier) Notify(n notify.Notification) error {
	f.sent = append(f.sent, n)
	return f.err
}

func (f *fakeNotifier) Withdraw(tag string) error {
	f.withdrawn = append(f.withdrawn, tag)
	return f.err
}

// toastModel builds a Model with one project, one tab and one pane, wired to a
// fake notifier with desktop notifications enabled and the terminal blurred.
func toastModel(t *testing.T) (*Model, *fakeNotifier, *PaneModel) {
	t.Helper()
	t.Setenv("QUIL_HOME", t.TempDir())

	m := newTestModel(t)
	f := &fakeNotifier{}
	m.SetDesktopNotifier(f, "quil", 4321)
	// Written into m.cfg, the ONE authority — there is deliberately no cached
	// copy on the Model for a Settings toggle to fall out of step with.
	m.cfg.Notification.Desktop = config.DesktopConfig{
		Enabled: true, Blocked: true, Done: true,
		Cooldown: "30s", RequireBlur: true,
	}
	m.termFocused = false

	pane := m.projects[0].tabs[0].Leaves()[0]
	return &m, f, pane
}

func TestRaiseAttentionToast_FiresOnBlockedRisingEdge(t *testing.T) {
	m, f, pane := toastModel(t)
	pane.blockedSince = time.Now()

	m.raiseAttentionToast(pane, m.projects[0], false, false)

	if len(f.sent) != 1 {
		t.Fatalf("sent %d toasts, want 1", len(f.sent))
	}
	if f.sent[0].Tag != pane.ID {
		t.Errorf("Tag = %q, want the pane id %q", f.sent[0].Tag, pane.ID)
	}
	if f.sent[0].Launch == "" {
		t.Error("Launch URI empty — the toast would not be clickable")
	}
	pid, paneID, err := notify.ParseActivateURI("quil", f.sent[0].Launch)
	if err != nil {
		t.Fatalf("toast Launch is not a parseable activation URI: %v", err)
	}
	if pid != 4321 || paneID != pane.ID {
		t.Errorf("Launch routes to pid=%d pane=%s, want 4321/%s", pid, paneID, pane.ID)
	}
}

func TestRaiseAttentionToast_SilentWhenAlreadyBlocked(t *testing.T) {
	m, f, pane := toastModel(t)
	pane.blockedSince = time.Now()

	m.raiseAttentionToast(pane, m.projects[0], true, false) // was ALREADY blocked

	if len(f.sent) != 0 {
		t.Errorf("sent %d toasts on a non-edge, want 0", len(f.sent))
	}
}

func TestRaiseAttentionToast_Gates(t *testing.T) {
	tests := []struct {
		name  string
		setup func(m *Model, p *PaneModel)
		want  int
	}{
		{"terminal focused", func(m *Model, p *PaneModel) { m.termFocused = true }, 0},
		{"focused but require_blur off", func(m *Model, p *PaneModel) {
			m.termFocused = true
			m.cfg.Notification.Desktop.RequireBlur = false
		}, 1},
		{"feature disabled", func(m *Model, p *PaneModel) {
			m.cfg.Notification.Desktop.Enabled = false
		}, 0},
		{"blocked kind off", func(m *Model, p *PaneModel) {
			m.cfg.Notification.Desktop.Blocked = false
		}, 0},
		{"no notifier installed", func(m *Model, p *PaneModel) { m.notifier = nil }, 0},
		{"muted pane", func(m *Model, p *PaneModel) { p.muted = true }, 0},
		{"within cooldown", func(m *Model, p *PaneModel) {
			p.lastToastAt = time.Now().Add(-5 * time.Second)
		}, 0},
		{"past cooldown", func(m *Model, p *PaneModel) {
			p.lastToastAt = time.Now().Add(-31 * time.Second)
		}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, f, pane := toastModel(t)
			pane.blockedSince = time.Now()
			tt.setup(m, pane)

			m.raiseAttentionToast(pane, m.projects[0], false, false)

			if len(f.sent) != tt.want {
				t.Errorf("sent %d toasts, want %d", len(f.sent), tt.want)
			}
		})
	}
}

// The cooldown is per pane and SHARED by both kinds: a pane that blocks and
// then finishes five seconds later is one event to a human, not two.
func TestRaiseAttentionToast_CooldownIsSharedAcrossKinds(t *testing.T) {
	m, f, pane := toastModel(t)

	pane.blockedSince = time.Now()
	m.raiseAttentionToast(pane, m.projects[0], false, false)

	pane.blockedSince = time.Time{}
	pane.unseen = true
	m.raiseAttentionToast(pane, m.projects[0], true, false)

	if len(f.sent) != 1 {
		t.Errorf("sent %d toasts, want 1 — the cooldown is shared by blocked and done", len(f.sent))
	}
}

// Project and pane names arrive from a daemon the user may not control, so the
// toast is a remote-text render surface like every other.
func TestRaiseAttentionToast_SanitizesRemoteNames(t *testing.T) {
	m, f, pane := toastModel(t)
	m.projects[0].Name = "safe\u202Ereversed"
	pane.Name = "pane\u009Bhere"
	pane.blockedSince = time.Now()

	m.raiseAttentionToast(pane, m.projects[0], false, false)

	if len(f.sent) != 1 {
		t.Fatalf("sent %d toasts, want 1", len(f.sent))
	}
	body := f.sent[0].Title + f.sent[0].Body
	for _, bad := range []string{"\u202E", "\u009B"} {
		if contains(body, bad) {
			t.Errorf("toast text leaked %q: %q", bad, body)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
```

**Before writing this test**, read an existing `internal/tui` test that builds a Model (for example `internal/tui/attention_test.go`) and reuse its construction helper. If `newTestModel` does not exist, create it in `notify_test.go` following that file's pattern, and confirm `PaneModel` really has a `muted` field — if the mute flag is named differently, use the real name.

- [ ] **Step 2: Run the test to verify it fails**

Run: `./scripts/dev.sh test ./internal/tui/`
Expected: FAIL — `m.SetDesktopNotifier undefined`.

- [ ] **Step 3: Add the Model and PaneModel fields**

In `internal/tui/pane.go`, beside `unseen` (near line 108):

```go
	// lastToastAt rate-limits desktop notifications for this pane. Per pane and
	// shared by both event kinds, matching the daemon's own per-pane bell
	// cooldown (Pane.LastBellEventAt).
	lastToastAt time.Time
```

In `internal/tui/model.go`, in the `Model` struct:

```go
	// notifier raises desktop toasts. Nil on every platform but Windows, and
	// nil on Windows until the user has run `quil notify setup` — so every
	// call site is a single nil check rather than a platform branch.
	notifier     desktopNotifier
	notifyScheme string
	notifyPID    int

	// There is deliberately NO cached DesktopConfig here.
	// raiseAttentionToast reads m.cfg.Notification.Desktop directly — the same
	// field the Settings row's setter writes — so the toggle applies live by
	// construction rather than by remembering to sync two copies. Two copies of
	// one value with independent writers is the shape behind both the
	// applyBrowseDir staleness bug and the PinnedAttention local-write bug.

	// outstandingToasts holds the pane ids that currently have a live toast.
	// Read by the withdrawal sweep; see sweepOutstandingToasts.
	outstandingToasts map[string]struct{}
```

- [ ] **Step 4: Write `internal/tui/notify.go`**

```go
package tui

import (
	"time"

	"github.com/artyomsv/quil/internal/logger"
	"github.com/artyomsv/quil/internal/notify"
)

// desktopNotifier is defined HERE, at the consumer, per the project's Go
// conventions — internal/notify returns a concrete implementation and this
// package names only the two methods it uses, which is also what makes the
// whole trigger path testable with a recording fake on Linux.
type desktopNotifier interface {
	Notify(notify.Notification) error
	Withdraw(tag string) error
}

// SetDesktopNotifier installs the notifier and the identity the activation URI
// must carry.
//
// An explicit setter rather than a NewModel parameter, matching
// SetRecentCWDs: roughly 46 tests build a Model directly, and threading a
// platform-dependent value through the constructor would make every one of
// them care about it.
func (m *Model) SetDesktopNotifier(n desktopNotifier, scheme string, pid int) {
	m.notifier = n
	m.notifyScheme = scheme
	m.notifyPID = pid
}

// raiseAttentionToast fires a desktop toast on a RISING attention edge.
//
// Called from the single derivation point in applyWorkTransition, deriving the
// edge from the before/after pair rather than hooking each write of
// blockedSince and unseen. There are three such writes today (workstate.go
// lines 350, 384 and 421); a fourth added later would silently emit nothing if
// this were wired per write site. The spinner beside it already works this way
// for the same reason.
func (m *Model) raiseAttentionToast(pane *PaneModel, proj *ProjectModel, wasBlocked, wasUnseen bool) {
	// Read from m.cfg, never from a cached copy — see the Model field comment.
	// This is also what makes the F1 → Settings toggle take effect immediately.
	cfg := m.cfg.Notification.Desktop
	if m.notifier == nil || !cfg.Enabled || pane == nil || proj == nil {
		return
	}
	// A muted pane is muted everywhere. The sidebar already honours this and a
	// toast is louder than a sidebar row, so honouring it here is the minimum.
	if pane.muted {
		return
	}
	// The whole premise is "you are not looking at Quil". RequireBlur is the
	// escape hatch for a terminal that never reports focus at all.
	if cfg.RequireBlur && m.termFocused {
		return
	}

	nowBlocked := !pane.blockedSince.IsZero()
	var kind string
	switch {
	case cfg.Blocked && nowBlocked && !wasBlocked:
		kind = "▲ Waiting for your input"
	case cfg.Done && pane.unseen && !wasUnseen:
		kind = "✓ Turn finished"
	default:
		return
	}

	now := time.Now()
	if !pane.lastToastAt.IsZero() && now.Sub(pane.lastToastAt) < cfg.CooldownDuration() {
		return
	}
	pane.lastToastAt = now

	// Sanitised HERE rather than inside internal/notify, because this package's
	// standing rule is that remote text is cleaned at RENDER and never in
	// state — a project name round-trips back to the daemon, so stripping it in
	// state would rewrite a name the user never edited. The toast is one more
	// render surface. internal/notify still escapes and bounds what it is
	// handed; the two passes cover different threats.
	title := sanitizeRemoteText(proj.Name)
	if pane.Name != "" {
		title += " · " + sanitizeRemoteText(pane.Name)
	}
	reason := kind
	if nowBlocked && pane.blockedReason != "" {
		reason = kind + " (" + sanitizeRemoteText(pane.blockedReason) + ")"
	}

	if m.outstandingToasts == nil {
		m.outstandingToasts = make(map[string]struct{}, 1)
	}
	m.outstandingToasts[pane.ID] = struct{}{}

	n := notify.Notification{
		Title:  title,
		Body:   reason,
		Tag:    pane.ID,
		Launch: notify.BuildActivateURI(m.notifyScheme, m.notifyPID, pane.ID),
	}
	if err := m.notifier.Notify(n); err != nil {
		// Logged, never retried and never surfaced. The sidebar event has
		// already landed, so the user is not missing the information — and a
		// retried toast is worse than a missing one.
		logger.Debug("notify: toast failed for pane %s: %v", pane.ID, err)
	}
}
```

Check the real name of `logger.Debug` and the mute field before writing — read `internal/logger/` and `internal/tui/pane.go` rather than assuming.

- [ ] **Step 5: Wire the single call site in `applyWorkTransition`**

In `internal/tui/workstate.go`, beside `wasWorking := pane.working` (line 242):

```go
	wasWorking := pane.working
	wasBlocked := !pane.blockedSince.IsZero()
	wasUnseen := pane.unseen
```

After the existing edge switch closes (after line 423, before the function's closing brace):

```go
	// One call site, deriving both edges from the before/after pair captured
	// above — see raiseAttentionToast for why this is not wired per write.
	m.raiseAttentionToast(pane, proj, wasBlocked, wasUnseen)
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `./scripts/dev.sh test ./internal/tui/`
Expected: PASS.

- [ ] **Step 7: Verify no regression**

Run: `./scripts/dev.sh test` and `./scripts/dev.sh vet`
Expected: clean. `applyWorkTransition` is heavily covered — read any failure carefully, since a break here means the edge capture disturbed existing spinner behaviour.

---

### Task 8: Withdrawal sweep

**Files:**
- Modify: `internal/tui/notify.go` (add the sweep)
- Modify: `internal/tui/model.go` (call it beside `ackFocusedPane`, line ~907)
- Test: `internal/tui/notify_test.go` (append)

**Interfaces:**
- Consumes: `Model.outstandingToasts`, `desktopNotifier.Withdraw` (Task 7)
- Produces: `func (m *Model) sweepOutstandingToasts()`

- [ ] **Step 1: Write the failing test**

Append to `internal/tui/notify_test.go`:

```go
// The rising edge has a single choke point. The falling edge does NOT —
// blockedSince and unseen are cleared in at least four places outside
// applyWorkTransition, so withdrawal is a sweep rather than a mirror of the
// emit. The typing path is the one that matters: approving a Bash/Edit/Write
// prompt fires no hook at all, so an edge-based withdrawal would leave the
// toast standing for the pane's whole remaining turn.
func TestSweepOutstandingToasts_WithdrawsWhenAttentionEnds(t *testing.T) {
	tests := []struct {
		name  string
		clear func(m *Model, p *PaneModel)
	}{
		{"user typed an answer", func(m *Model, p *PaneModel) { p.answerBlockedByInput() }},
		{"pane focused, unseen cleared", func(m *Model, p *PaneModel) { p.unseen = false }},
		{"clear attention menu row", func(m *Model, p *PaneModel) {
			p.blockedSince = time.Time{}
			p.unseen = false
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, f, pane := toastModel(t)
			pane.blockedSince = time.Now()
			m.raiseAttentionToast(pane, m.projects[0], false, false)
			if len(f.sent) != 1 {
				t.Fatalf("setup: sent %d toasts, want 1", len(f.sent))
			}

			tt.clear(m, pane)
			m.sweepOutstandingToasts()

			if len(f.withdrawn) != 1 || f.withdrawn[0] != pane.ID {
				t.Errorf("withdrawn = %v, want [%s]", f.withdrawn, pane.ID)
			}
			if _, still := m.outstandingToasts[pane.ID]; still {
				t.Error("swept tag must be dropped from the outstanding set")
			}
		})
	}
}

func TestSweepOutstandingToasts_KeepsLiveToasts(t *testing.T) {
	m, f, pane := toastModel(t)
	pane.blockedSince = time.Now()
	m.raiseAttentionToast(pane, m.projects[0], false, false)

	m.sweepOutstandingToasts()

	if len(f.withdrawn) != 0 {
		t.Errorf("withdrew %v while the pane is still blocked", f.withdrawn)
	}
}

// A destroyed pane cannot clear its own flags, so the sweep must treat "gone"
// as a reason to withdraw.
func TestSweepOutstandingToasts_WithdrawsForDestroyedPane(t *testing.T) {
	m, f, pane := toastModel(t)
	pane.blockedSince = time.Now()
	m.raiseAttentionToast(pane, m.projects[0], false, false)

	m.outstandingToasts["pane-deadbeef"] = struct{}{}
	m.sweepOutstandingToasts()

	found := false
	for _, tag := range f.withdrawn {
		if tag == "pane-deadbeef" {
			found = true
		}
	}
	if !found {
		t.Errorf("withdrawn = %v, want it to include the vanished pane", f.withdrawn)
	}
}

func TestSweepOutstandingToasts_NoOpWhenEmpty(t *testing.T) {
	m, f, _ := toastModel(t)
	m.sweepOutstandingToasts()
	if len(f.withdrawn) != 0 {
		t.Errorf("withdrew %v from an empty set", f.withdrawn)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `./scripts/dev.sh test ./internal/tui/`
Expected: FAIL — `m.sweepOutstandingToasts undefined`.

- [ ] **Step 3: Write the implementation**

Append to `internal/tui/notify.go`:

```go
// sweepOutstandingToasts withdraws toasts whose pane no longer needs attention.
//
// A sweep rather than a hook on each clear site, because the falling edge has
// no choke point the way the rising one does. blockedSince and unseen are
// cleared in at least four places outside applyWorkTransition:
//
//   - PaneModel.answerBlockedByInput — the user typed an answer
//   - Model.ackFocusedPane          — the user focused the pane
//   - ctxmenu.go's "Clear attention" row
//   - pane destruction, which has no transition at all
//
// The first is both the commonest and the worst for an edge-based design:
// approving a Bash/Edit/Write prompt fires NO hook (promptToolMatcher covers
// only AskUserQuestion and ExitPlanMode), so applyWorkTransition is never
// reached and the toast would stand until the turn's Stop, minutes away.
//
// The sweep also covers a fifth clear path added later without anyone
// remembering this file, which a set of hooks cannot.
func (m *Model) sweepOutstandingToasts() {
	// The overwhelmingly common state, and this runs on every Update including
	// the 100 ms spinner tick — so the empty case must cost nothing.
	if len(m.outstandingToasts) == 0 || m.notifier == nil {
		return
	}
	for paneID := range m.outstandingToasts {
		pane, _, _ := m.findPaneAndTab(paneID)
		// A vanished pane cannot clear its own flags, so "gone" withdraws too.
		if pane != nil && (!pane.blockedSince.IsZero() || pane.unseen) {
			continue
		}
		if err := m.notifier.Withdraw(paneID); err != nil {
			logger.Debug("notify: withdraw failed for pane %s: %v", paneID, err)
		}
		delete(m.outstandingToasts, paneID)
	}
}
```

- [ ] **Step 4: Wire it at the Update choke point**

In `internal/tui/model.go`, immediately after `m.ackFocusedPane()` (line ~907):

```go
	// Beside ackFocusedPane deliberately: this is the one point every message
	// passes through, and the sweep's own emptiness check makes it free in the
	// common case.
	m.sweepOutstandingToasts()
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `./scripts/dev.sh test ./internal/tui/`
Expected: PASS.

- [ ] **Step 6: Verify**

Run: `./scripts/dev.sh test` and `./scripts/dev.sh vet`
Expected: clean.

---

### Task 9: Windows toast emission

**Files:**
- Create: `internal/notify/notify_windows.go`
- Test: `internal/notify/notify_windows_test.go`

**Interfaces:**
- Consumes: `Notification`, `BuildToastXML` (Task 3), `Options`, `Notifier`, `ErrNotRegistered` (Task 4), and the IIDs confirmed in Task 1
- Produces: `func New(opts Options) (Notifier, error)` for `//go:build windows`

- [ ] **Step 1: Write the failing test**

Create `internal/notify/notify_windows_test.go`:

```go
//go:build windows

package notify

import (
	"errors"
	"testing"
)

// Runs only on a real Windows host. Build with:
//   docker run --rm -v "$PWD":/src -w /src -e GOOS=windows -e GOARCH=amd64 \
//     golang:1.25 go test -c -o /src/notify_win_test.exe ./internal/notify/
// then run ./notify_win_test.exe -test.v on the host.

func TestNew_RefusesUnregisteredAUMID(t *testing.T) {
	n, err := New(Options{AUMID: "artyomsv.quil.nonexistent.test", Scheme: "quil-test"})
	if err == nil {
		if n != nil {
			_ = n.Close()
		}
		t.Fatal("New() accepted an AUMID with no backing Start Menu shortcut")
	}
	if !errors.Is(err, ErrNotRegistered) {
		t.Errorf("err = %v, want ErrNotRegistered", err)
	}
}

// Smoke test: requires `quil notify setup` (dev variant) to have run first.
// Skipped otherwise so the suite stays green on a machine without it.
func TestNotify_ShowsAndWithdraws(t *testing.T) {
	opts := Variant(true)
	if ok, _ := Registered(opts); !ok {
		t.Skip("dev variant not registered — run 'quil-dev.exe notify setup' to exercise this")
	}
	n, err := New(opts)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	defer n.Close()

	sent := Notification{
		Title:  "quil test",
		Body:   "TEST-CANARY — safe to dismiss",
		Tag:    "pane-00000000",
		Launch: BuildActivateURI(opts.Scheme, 1, "pane-00000000"),
	}
	if err := n.Notify(sent); err != nil {
		t.Fatalf("Notify() = %v", err)
	}
	if err := n.Withdraw(sent.Tag); err != nil {
		t.Fatalf("Withdraw() = %v", err)
	}
}
```

The smoke test's body is deliberately self-labelling (`TEST-CANARY`) so a toast that escapes into Action Center cannot be mistaken for a real one.

- [ ] **Step 2: Build the Windows test binary and run it**

```bash
docker run --rm -v "$PWD":/src -w /src -e GOOS=windows -e GOARCH=amd64 golang:1.25 \
  go test -c -o /src/notify_win_test.exe ./internal/notify/
```
Expected: FAIL to build — `undefined: New` for windows, `undefined: Registered`.

- [ ] **Step 3: Write the implementation**

Create `internal/notify/notify_windows.go` using the exact call sequence and IIDs confirmed by the Task 1 spike. The structure:

```go
//go:build windows

package notify

import (
	"fmt"
	"sync"
)

type winNotifier struct {
	mu    sync.Mutex
	opts  Options
	// handles obtained from RoGetActivationFactory, per the Task 1 spike
}

// New builds a toast notifier for this AUMID.
//
// Windows will not display a toast from an unpackaged executable whose AUMID
// has no backing Start Menu shortcut, so this refuses up front rather than
// letting every Notify fail silently later.
func New(opts Options) (Notifier, error) {
	if ok, err := Registered(opts); err != nil {
		return nil, fmt.Errorf("notify: checking registration: %w", err)
	} else if !ok {
		return nil, ErrNotRegistered
	}
	// RoInitialize(RO_INIT_MULTITHREADED), then cache the two activation
	// factories. Guard RoInitialize with a sync.Once at package scope: it is
	// per-process, and the listener goroutine may also be running.
	panic("implement from the Task 1 spike findings")
}

func (w *winNotifier) Notify(n Notification) error {
	// 1. RoActivateInstance("Windows.Data.Xml.Dom.XmlDocument")
	// 2. xmlDoc.LoadXml(BuildToastXML(n))
	// 3. toastFactory.CreateToastNotification(xmlDoc)
	// 4. toast.put_Tag(n.Tag); toast.put_Group(w.opts.AUMID)
	// 5. notifier.Show(toast)
	//
	// Tag and Group are what make Withdraw addressable later. Group is the
	// AUMID rather than a constant so a dev and a prod instance can never
	// withdraw each other's toasts.
	panic("implement from the Task 1 spike findings")
}

func (w *winNotifier) Withdraw(tag string) error {
	// statics2.get_History().RemoveGroupedTagWithId(tag, w.opts.AUMID, w.opts.AUMID)
	panic("implement from the Task 1 spike findings")
}

func (w *winNotifier) Close() error { panic("implement") }
```

Replace every `panic` with the real interop. Serialise all WinRT calls behind `w.mu` — the notifier is reached from the Bubble Tea Update goroutine and, once Task 11 lands, potentially from the listener's.

`Registered` is implemented in Task 10; if that task has not run yet, stub it there first — the two are separable but `New` depends on it.

- [ ] **Step 4: Rebuild and run on the host**

```bash
docker run --rm -v "$PWD":/src -w /src -e GOOS=windows -e GOARCH=amd64 golang:1.25 \
  go test -c -o /src/notify_win_test.exe ./internal/notify/
./notify_win_test.exe -test.v
```
Expected: `TestNew_RefusesUnregisteredAUMID` PASS; the smoke test skips until Task 10 has registered the dev variant.

- [ ] **Step 5: Verify Linux is untouched**

Run: `./scripts/dev.sh test ./internal/notify/` and `./scripts/dev.sh vet`
Expected: PASS — the Linux build still uses `notify_other.go`.

Delete `notify_win_test.exe`.

---

### Task 10: Windows setup, removal, and status

**Files:**
- Create: `internal/notify/setup_windows.go`
- Create: `internal/notify/setup_other.go`
- Test: `internal/notify/setup_windows_test.go`

**Interfaces:**
- Consumes: `Options` (Task 4)
- Produces:
  - `func Setup(opts Options, exePath string) (written []string, err error)`
  - `func Remove(opts Options) (removed []string, err error)`
  - `func Registered(opts Options) (bool, error)`
  - `func ShortcutPath(opts Options) (string, error)`

- [ ] **Step 1: Write the failing test**

Create `internal/notify/setup_windows_test.go`:

```go
//go:build windows

package notify

import (
	"os"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// A scratch variant so the test never disturbs a real prod or dev
// registration on the developer's own machine.
func scratchVariant() Options {
	return Options{AUMID: "artyomsv.quil.selftest", Scheme: "quil-selftest"}
}

func TestSetupRemove_RoundTrip(t *testing.T) {
	opts := scratchVariant()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() = %v", err)
	}
	t.Cleanup(func() { _, _ = Remove(opts) })

	written, err := Setup(opts, exe)
	if err != nil {
		t.Fatalf("Setup() = %v", err)
	}
	if len(written) < 2 {
		t.Errorf("Setup reported %v, want both the shortcut and the registry key", written)
	}

	if ok, err := Registered(opts); err != nil || !ok {
		t.Fatalf("Registered() = %v, %v after Setup", ok, err)
	}

	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Classes\`+opts.Scheme+`\shell\open\command`, registry.QUERY_VALUE)
	if err != nil {
		t.Fatalf("protocol command key missing: %v", err)
	}
	cmd, _, err := k.GetStringValue("")
	k.Close()
	if err != nil {
		t.Fatalf("reading command value: %v", err)
	}
	if !contains(cmd, exe) || !contains(cmd, "%1") {
		t.Errorf("command = %q, want it to invoke %q with %%1", cmd, exe)
	}

	// Remove must be a TRUE inverse. A setup command the user cannot undo is
	// one they should not have been asked to run.
	if _, err := Remove(opts); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	if ok, _ := Registered(opts); ok {
		t.Error("Registered() still true after Remove")
	}
	if _, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Classes\`+opts.Scheme, registry.QUERY_VALUE); err == nil {
		t.Error("protocol key survived Remove")
	}
	sc, _ := ShortcutPath(opts)
	if _, err := os.Stat(sc); err == nil {
		t.Errorf("shortcut %s survived Remove", sc)
	}
}

func TestRemove_IsIdempotent(t *testing.T) {
	opts := scratchVariant()
	if _, err := Remove(opts); err != nil {
		t.Errorf("Remove() on an unregistered variant = %v, want nil", err)
	}
}

func TestShortcutPath_DevAndProdDiffer(t *testing.T) {
	prod, err := ShortcutPath(Variant(false))
	if err != nil {
		t.Fatalf("ShortcutPath(prod) = %v", err)
	}
	dev, err := ShortcutPath(Variant(true))
	if err != nil {
		t.Fatalf("ShortcutPath(dev) = %v", err)
	}
	if prod == dev {
		t.Fatalf("dev and prod share a shortcut path %q — a dev setup would overwrite production", prod)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Build and run on the host to verify it fails**

```bash
docker run --rm -v "$PWD":/src -w /src -e GOOS=windows -e GOARCH=amd64 golang:1.25 \
  go test -c -o /src/notify_win_test.exe ./internal/notify/
```
Expected: FAIL to build — `undefined: Setup`.

- [ ] **Step 3: Write `setup_other.go`**

```go
//go:build !windows

package notify

// Setup, Remove, Registered and ShortcutPath exist on every platform so
// cmd/quil needs no build tags of its own; they refuse where there is nothing
// to register.
func Setup(opts Options, exePath string) ([]string, error) { return nil, ErrUnsupported }
func Remove(opts Options) ([]string, error)                { return nil, ErrUnsupported }
func Registered(opts Options) (bool, error)                { return false, nil }
func ShortcutPath(opts Options) (string, error)            { return "", ErrUnsupported }
```

`Registered` returns `(false, nil)` rather than the error: it answers a question that has a true answer everywhere ("no"), and returning an error would make every caller special-case a platform that simply has no registration.

- [ ] **Step 4: Write `setup_windows.go`**

Implement these four functions:

- `ShortcutPath(opts)` — `%APPDATA%\Microsoft\Windows\Start Menu\Programs\` plus `Quil.lnk` for the prod AUMID and `Quil (dev).lnk` otherwise. Derive the filename from `opts.AUMID` so the two can never collide.
- `Setup(opts, exePath)` — creates the shortcut via `IShellLink` + `IPersistFile`, then sets `System.AppUserModel.ID` on it through `IPropertyStore` with `PKEY_AppUserModel_ID` (`{9F4C2855-9F79-4B39-A8D0-E1D42DE1D5F3}`, PID 5). Then writes:

  ```
  HKCU\Software\Classes\<scheme>            (Default) = "URL:Quil Protocol"
  HKCU\Software\Classes\<scheme>            "URL Protocol" = ""
  HKCU\Software\Classes\<scheme>\shell\open\command
                                            (Default) = "\"<exePath>\" activate \"%1\""
  ```

  Return the list of what was written so the command can print it.
- `Remove(opts)` — deletes the shortcut and the whole `<scheme>` key tree. Idempotent: a missing artifact is not an error.
- `Registered(opts)` — true when both the shortcut exists and the command key resolves.

Set the shortcut's icon from the executable itself (`quil.exe` already carries the brand mark via `winres/`), so the toast shows the Quil icon with no extra asset.

- [ ] **Step 5: Rebuild and run on the host**

```bash
docker run --rm -v "$PWD":/src -w /src -e GOOS=windows -e GOARCH=amd64 golang:1.25 \
  go test -c -o /src/notify_win_test.exe ./internal/notify/
./notify_win_test.exe -test.v -test.run 'TestSetup|TestRemove|TestShortcut'
```
Expected: PASS.

- [ ] **Step 6: Verify no scratch artifacts survived**

Check by hand that `artyomsv.quil.selftest` left nothing:

```powershell
Get-ChildItem "$env:APPDATA\Microsoft\Windows\Start Menu\Programs" -Filter "Quil*"
Get-Item "HKCU:\Software\Classes\quil-selftest" -ErrorAction SilentlyContinue
```
Expected: no `selftest` shortcut, no `quil-selftest` key. Delete `notify_win_test.exe`.

- [ ] **Step 7: Verify Linux**

Run: `./scripts/dev.sh test` and `./scripts/dev.sh vet`
Expected: clean.

---

### Task 11: Named-pipe activation listener and client

**Files:**
- Create: `internal/notify/listener_windows.go`
- Create: `internal/notify/listener_other.go`
- Test: `internal/notify/listener_windows_test.go`

**Interfaces:**
- Consumes: `ValidPaneID` (Task 2)
- Produces:
  - `func PipeName(pid int) string`
  - `func Listen(pid int, onActivate func(paneID string)) (io.Closer, error)`
  - `func SendActivate(pid int, paneID string) error`

- [ ] **Step 1: Write the failing test**

Create `internal/notify/listener_windows_test.go`:

```go
//go:build windows

package notify

import (
	"os"
	"testing"
	"time"
)

func TestListenSendActivate_RoundTrip(t *testing.T) {
	pid := os.Getpid()
	got := make(chan string, 1)

	srv, err := Listen(pid, func(paneID string) { got <- paneID })
	if err != nil {
		t.Fatalf("Listen() = %v", err)
	}
	defer srv.Close()

	if err := SendActivate(pid, "pane-0a1b2c3d"); err != nil {
		t.Fatalf("SendActivate() = %v", err)
	}
	select {
	case id := <-got:
		if id != "pane-0a1b2c3d" {
			t.Errorf("received %q, want pane-0a1b2c3d", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("activation never reached the listener")
	}
}

// The pipe is reachable by any same-user process, so the listener validates
// independently of the URI parser rather than trusting that quil activate was
// the writer.
func TestListen_RejectsMalformedPaneIDs(t *testing.T) {
	pid := os.Getpid()
	got := make(chan string, 4)

	srv, err := Listen(pid, func(paneID string) { got <- paneID })
	if err != nil {
		t.Fatalf("Listen() = %v", err)
	}
	defer srv.Close()

	for _, bad := range []string{"", "../../etc/passwd", "pane-BADBEEF", "pane-0a1b2c3d4"} {
		_ = writeRawToPipe(t, pid, bad)
	}
	// A good one behind them proves the listener is still alive rather than
	// merely quiet — a listener that died on the first bad frame would pass a
	// test that only asserted silence.
	if err := SendActivate(pid, "pane-11111111"); err != nil {
		t.Fatalf("SendActivate() after bad frames = %v", err)
	}
	select {
	case id := <-got:
		if id != "pane-11111111" {
			t.Errorf("first delivered id = %q, want the only well-formed one", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("listener stopped serving after a malformed frame")
	}
}

func TestSendActivate_NoListenerIsNotAnError(t *testing.T) {
	// Clicking a toast whose TUI has since exited must not pop an error
	// window. The caller exits 0; SendActivate reports the condition so the
	// caller can stay silent about it.
	err := SendActivate(999999, "pane-0a1b2c3d")
	if err == nil {
		t.Skip("a process with that pid unexpectedly has a listener")
	}
}
```

`writeRawToPipe` is a test helper you write in this file: it dials `PipeName(pid)` and writes the raw string with no validation, so the listener's own guard is what is under test.

- [ ] **Step 2: Build and run on the host to verify it fails**

```bash
docker run --rm -v "$PWD":/src -w /src -e GOOS=windows -e GOARCH=amd64 golang:1.25 \
  go test -c -o /src/notify_win_test.exe ./internal/notify/
```
Expected: FAIL to build — `undefined: Listen`.

- [ ] **Step 3: Write `listener_other.go`**

```go
//go:build !windows

package notify

import "io"

func PipeName(pid int) string { return "" }

func Listen(pid int, onActivate func(paneID string)) (io.Closer, error) {
	return nil, ErrUnsupported
}

func SendActivate(pid int, paneID string) error { return ErrUnsupported }
```

- [ ] **Step 4: Write `listener_windows.go`**

Requirements:

- `PipeName(pid)` returns `\\.\pipe\quil-activate-<pid>`.
- `Listen` creates the pipe with a security descriptor restricting access to the **current user only** — do not rely on the default. Each accepted connection is read to EOF with a small read cap (64 bytes is ample for a pane ID; a longer frame is refused rather than buffered), validated with `ValidPaneID`, and dispatched to `onActivate`. A malformed frame closes that connection and the accept loop continues — the second test above exists to pin that.
- Serve connections in a goroutine, with a clean shutdown path via the returned `io.Closer` (per the project's Go conventions: never launch a goroutine without one).
- `SendActivate` dials with a short timeout, writes the pane ID, closes, and returns an error if no listener exists. Callers translate that into a silent exit.

The pane ID is validated here **and** in `ParseActivateURI`, deliberately: the pipe is independently reachable, so the URI parser is not its gatekeeper. The pipe carries a pane ID and nothing else — it is not a command channel, which is what caps a hostile caller at moving the cursor.

- [ ] **Step 5: Rebuild and run on the host**

```bash
docker run --rm -v "$PWD":/src -w /src -e GOOS=windows -e GOARCH=amd64 golang:1.25 \
  go test -c -o /src/notify_win_test.exe ./internal/notify/
./notify_win_test.exe -test.v -test.run 'TestListen|TestSendActivate'
```
Expected: PASS. Delete `notify_win_test.exe`.

- [ ] **Step 6: Verify Linux**

Run: `./scripts/dev.sh test` and `./scripts/dev.sh vet`
Expected: clean.

---

### Task 12: CLI commands, TUI wiring, and documentation

**Files:**
- Create: `cmd/quil/notify.go`
- Modify: `cmd/quil/main.go:109-153` (subcommand dispatch), and the `launchTUI` body near lines 537 and 679
- Modify: `internal/tui/model.go` (`activatePaneMsg` arm)
- Modify: `docs/configuration.md`, `docs/features.md`, `docs/troubleshooting.md`, `.claude/CLAUDE.md`

**Interfaces:**
- Consumes: everything above
- Produces: `tui.ActivatePaneMsg{PaneID string}` — exported because `cmd/quil` constructs it for `p.Send`

- [ ] **Step 1: Write the failing test for the message arm**

Append to `internal/tui/notify_test.go`:

```go
// The activation message must route through jumpToPane rather than setting
// the active pane by hand. That function performs notes-mode teardown before
// the tab moves, the sidebarScroll reset ordering, syncActiveDest and
// notifyTabSwitch for lazily-restored tabs — roughly fifty lines of
// constraints, each of which was a bug first.
func TestActivatePaneMsg_JumpsToTheNamedPane(t *testing.T) {
	m, _, _ := toastModel(t)
	target := addSecondProjectWithPane(t, m) // helper: see below

	updated, _ := m.Update(ActivatePaneMsg{PaneID: target.ID})
	got := updated.(Model)

	tab := got.activeTabModel()
	if tab == nil || tab.ActivePane != target.ID {
		t.Errorf("active pane = %v, want %s", tab, target.ID)
	}
}

func TestActivatePaneMsg_IgnoresUnknownPane(t *testing.T) {
	m, _, _ := toastModel(t)
	before := m.activeProject

	updated, _ := m.Update(ActivatePaneMsg{PaneID: "pane-deadbeef"})
	got := updated.(Model)

	if got.activeProject != before {
		t.Errorf("activeProject moved to %d for an unknown pane", got.activeProject)
	}
}
```

Write `addSecondProjectWithPane` in this file, building a second project and tab the same way the existing `internal/tui` tests do — read `attention_test.go` first and follow its construction so there is one way to build fixtures, not two.

- [ ] **Step 2: Run the test to verify it fails**

Run: `./scripts/dev.sh test ./internal/tui/`
Expected: FAIL — `undefined: ActivatePaneMsg`.

- [ ] **Step 3: Add the message and its Update arm**

In `internal/tui/notify.go`:

```go
// ActivatePaneMsg asks the TUI to focus a pane by id.
//
// Exported because cmd/quil constructs it from the activation listener and
// delivers it with Program.Send. It carries a pane id and nothing else: the
// path from a registry-reachable URI to this message must not be able to name
// a command, an argument or a filesystem path.
type ActivatePaneMsg struct{ PaneID string }
```

In `Update`, beside the other message arms:

```go
	case ActivatePaneMsg:
		// Validated a third time. The value has already passed ParseActivateURI
		// and the pipe listener, but this is the boundary that actually moves
		// the user's focus, and jumpToPane is reached from five other callers
		// that owe it nothing.
		if !notify.ValidPaneID(msg.PaneID) {
			return m, nil
		}
		_, cmd := m.jumpToPane(msg.PaneID)
		return m, cmd
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `./scripts/dev.sh test ./internal/tui/`
Expected: PASS.

- [ ] **Step 5: Write `cmd/quil/notify.go`**

```go
package main

import (
	"fmt"
	"os"

	"github.com/artyomsv/quil/internal/notify"
)

// notifyVariant picks the registration namespace for THIS binary. buildDevMode
// is the same ldflag that redirects QUIL_HOME — but the toast artifacts live
// outside QUIL_HOME, so they are the one thing dev isolation does not get for
// free.
func notifyVariant() notify.Options { return notify.Variant(buildDevMode == "true") }

func handleNotify() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: quil notify [setup|status]  (setup accepts --remove)")
		os.Exit(1)
	}
	opts := notifyVariant()
	switch os.Args[2] {
	case "setup":
		if len(os.Args) > 3 && os.Args[3] == "--remove" {
			runNotifyRemove(opts)
			return
		}
		runNotifySetup(opts)
	case "status":
		runNotifyStatus(opts)
	default:
		fmt.Fprintf(os.Stderr, "unknown notify command: %s\n", os.Args[2])
		os.Exit(1)
	}
}
```

`runNotifySetup` resolves `os.Executable()`, calls `notify.Setup`, and — on success — prints **exactly what it wrote and how to undo it**, then the reminder that `[notification.desktop] enabled` must also be set:

```
Registered desktop notifications for quil.

  Shortcut   C:\Users\you\AppData\Roaming\...\Programs\Quil.lnk
  Protocol   HKCU\Software\Classes\quil

Enable them in ~/.quil/config.toml:

  [notification.desktop]
  enabled = true

Undo with:  quil notify setup --remove
```

`runNotifyRemove` prints what it removed. `runNotifyStatus` prints registration state, the config switch, and — read from the config only, since this process is not the TUI — a line explaining that focus-reporting detection is reported by the running TUI in its log. On non-Windows all three print a single sentence and exit 1.

Add `handleActivate`, which is the protocol handler:

```go
// handleActivate is the quil:// protocol handler. It is invoked by Windows
// when the user clicks a toast.
//
// It does exactly one thing: parse, validate, forward. There is deliberately no
// path from here to spawning a pane, sending input, or running a command —
// registering a URI scheme makes this reachable by any local process and, behind
// a browser confirmation, by a web page. The ceiling is "the cursor moves to a
// pane the user already owns", and that ceiling is the design constraint.
func handleActivate() {
	if len(os.Args) < 3 {
		os.Exit(1)
	}
	opts := notifyVariant()
	pid, paneID, err := notify.ParseActivateURI(opts.Scheme, os.Args[2])
	if err != nil {
		os.Exit(1)
	}
	// A TUI that exited between the toast and the click leaves no listener.
	// Exit 0 silently: a stale toast must never pop an error window.
	_ = notify.SendActivate(pid, paneID)
}
```

- [ ] **Step 6: Wire the subcommands and the TUI**

In `cmd/quil/main.go`, add to the `switch os.Args[1]` block (near line 110):

```go
		case "notify":
			handleNotify()
			return
		case "activate":
			handleActivate()
			return
```

In `launchTUI`, after `model := tui.NewModel(...)` (line 537) and before `tea.NewProgram`:

```go
	notifyOpts := notifyVariant()
	if n, err := notify.New(notifyOpts); err != nil {
		// Not fatal and not a dialog. A dialog about notifications is the
		// wrong medicine, and the sidebar keeps working regardless.
		log.Printf("desktop notifications unavailable: %v", err)
	} else if n != nil {
		model.SetDesktopNotifier(n, notifyOpts.Scheme, os.Getpid())
	}
```

After `p := tea.NewProgram(model)` (line 679) — the listener needs the Program handle, which does not exist until here:

```go
	if lis, err := notify.Listen(os.Getpid(), func(paneID string) {
		p.Send(tui.ActivatePaneMsg{PaneID: paneID})
	}); err != nil {
		// Toast anyway. The primary value is "an agent needs you"; routing is
		// the bonus, and disabling the whole feature over a pipe failure is the
		// larger silent loss.
		log.Printf("toast activation listener unavailable: %v", err)
	} else if lis != nil {
		defer lis.Close()
	}
```

No config plumbing is needed here: `raiseAttentionToast` reads `m.cfg.Notification.Desktop`, which `NewModel` already populated from the `cfg` passed to it. If you find yourself adding a config field to the Model, stop — that is the cached copy this design deliberately does not have.

- [ ] **Step 7: Build and verify the whole suite**

```bash
./scripts/dev.sh build
./scripts/dev.sh test
./scripts/dev.sh vet
```
Expected: all clean, six binaries built.

- [ ] **Step 8: Manual end-to-end verification in dev mode**

1. `./quil-dev.exe notify setup` — confirm it prints both artifacts.
2. Add to the dev config (`.quil/config.toml`):
   ```toml
   [notification.desktop]
   enabled = true
   ```
3. Launch `./scripts/quil-dev.ps1`. **Confirm `[dev]` appears in the status bar** before doing anything else.
4. Start a claude-code pane, give it a task that will ask permission, then click away to another window.
5. Confirm a toast appears naming the project and pane.
6. Click the toast. Confirm Quil is now on that project, tab and pane. (The terminal window will not raise itself — that is the agreed follow-up.)
7. Answer the prompt. Confirm the toast disappears from Action Center.
8. Focus the terminal and trigger another park. Confirm **no** toast fires.
9. `./quil-dev.exe notify setup --remove`. Confirm the shortcut and `HKCU\Software\Classes\quil-dev` are both gone, and that production's `quil` registration — if you have one — is untouched.

- [ ] **Step 9: Update documentation**

- `docs/configuration.md` — the `[notification.desktop]` table with all five keys and their defaults, noting that `enabled` is true by default and that `quil notify setup` is still required on Windows.
- `docs/features.md` — a Notifications row: what triggers a toast, that it is Windows-only, that clicking routes to the pane, and that F1 → Settings toggles it live.
- `docs/troubleshooting.md` — "No desktop notifications", in the order a user should check them: (1) run `quil notify setup` — this is the usual cause, since `enabled` is already true by default and registration is the remaining gate; (2) check F1 → Settings, which reads `on (run notify setup)` in exactly that state; (3) the focus-reporting caveat — a terminal without DEC 1004 support reports nothing, so the blur gate suppresses everything, and `require_blur = false` is the workaround.
- `docs/roadmap.md` — strike M17 item 14 and priority row 7, noting the Windows-only scope and that window raising remains open.
- `.claude/CLAUDE.md` — one line in the Architecture list for `internal/notify/`. Keep it short; the detail belongs in a scoped rule if it grows.

---

### Task 13: Settings dialog toggle

Last because it depends on `notify.Registered()` (Task 10) for its tri-state
display, and because it carries the plan's single commit.

**Files:**
- Modify: `internal/tui/dialog.go:191` (`settingsFields`)
- Test: `internal/tui/dialog_notify_test.go`

**Interfaces:**
- Consumes: `notify.Registered`, `notify.Variant` (Tasks 4, 10), `config.DesktopConfig` (Task 5)
- Produces: one `settingsField` row labelled `Desktop notifications`

- [ ] **Step 1: Write the failing test**

Create `internal/tui/dialog_notify_test.go`:

```go
package tui

import (
	"strings"
	"testing"
)

func desktopRow(t *testing.T) settingsField {
	t.Helper()
	for _, f := range settingsFields() {
		if f.label == "Desktop notifications" {
			return f
		}
	}
	t.Fatal("no 'Desktop notifications' row in settingsFields()")
	return settingsField{}
}

func TestSettingsDesktopRow_TogglesAndPersists(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	m := newTestModel(t)
	m.cfg.Notification.Desktop.Enabled = true
	m.configChanged = false

	row := desktopRow(t)
	if !row.isBool {
		t.Error("the row must be a bool toggle, not a text field")
	}

	row.set(&m, "")
	if m.cfg.Notification.Desktop.Enabled {
		t.Error("set() did not flip Enabled")
	}
	// Without this the edit is silently discarded when the TUI exits — the
	// defect that once dropped every Settings change except the disclaimer.
	if !m.configChanged {
		t.Error("set() must mark configChanged so the value reaches config.toml")
	}

	row.set(&m, "")
	if !m.cfg.Notification.Desktop.Enabled {
		t.Error("set() is not a toggle — a second call did not flip it back")
	}
}

// With enabled defaulting true, "on but not registered" is the DEFAULT state on
// a fresh Windows install, not an edge case. Rendering a bare "on" there would
// tell the user something is working when nothing is — the same rule the
// Sidebar width setter states: the dialog must never show a value the system is
// not actually using.
func TestSettingsDesktopRow_ReportsStateNotFlag(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	m := newTestModel(t)
	row := desktopRow(t)

	m.cfg.Notification.Desktop.Enabled = false
	if got := row.get(&m); got != "off" {
		t.Errorf("disabled row = %q, want \"off\"", got)
	}

	m.cfg.Notification.Desktop.Enabled = true
	got := row.get(&m)
	switch {
	case got == "on":
		// Windows with registration present.
	case strings.Contains(got, "notify setup"):
		// Enabled but unregistered — must name the remedy.
	case got == "unsupported":
		// Not Windows. This is the CI case.
	default:
		t.Errorf("enabled row = %q, want on / on (run notify setup) / unsupported", got)
	}
}

// CI runs Linux, where there is nothing to register and the row must say so
// rather than claiming toasts are on.
func TestSettingsDesktopRow_UnsupportedOffWindows(t *testing.T) {
	if runtimeIsWindows() {
		t.Skip("this asserts the non-Windows rendering")
	}
	t.Setenv("QUIL_HOME", t.TempDir())
	m := newTestModel(t)
	m.cfg.Notification.Desktop.Enabled = true
	if got := desktopRow(t).get(&m); got != "unsupported" {
		t.Errorf("row on a non-Windows host = %q, want \"unsupported\"", got)
	}
}
```

Write `runtimeIsWindows()` as `runtime.GOOS == "windows"` in this file.

- [ ] **Step 2: Run the test to verify it fails**

Run: `./scripts/dev.sh test ./internal/tui/`
Expected: FAIL — `no 'Desktop notifications' row in settingsFields()`.

- [ ] **Step 3: Add the row**

Append to the slice returned by `settingsFields()` in `internal/tui/dialog.go`:

```go
		{
			// Reports STATE, not the flag. With Enabled defaulting true,
			// enabled-but-unregistered is the default on a fresh Windows
			// install — a bare "on" there would claim toasts are working when
			// no toast can be displayed at all. Same rule the Sidebar width
			// setter states: never show a value the system is not using.
			//
			// The row deliberately does NOT perform registration. Writing a
			// Start Menu shortcut and an HKCU key as a side effect of a config
			// toggle is exactly the auto-register behaviour this design
			// rejected; it names the command instead.
			//
			// Applies LIVE, unlike most rows here: raiseAttentionToast reads
			// m.cfg.Notification.Desktop on every edge, so there is no apply
			// step. An on/off switch that did nothing until relaunch would read
			// as a broken dialog — the same reason Sidebar width is live.
			label: "Desktop notifications",
			get: func(m *Model) string {
				if !m.cfg.Notification.Desktop.Enabled {
					return "off"
				}
				opts := notify.Variant(m.devMode)
				ok, err := notify.Registered(opts)
				switch {
				case err != nil:
					return "on (state unknown)"
				case ok:
					return "on"
				case runtime.GOOS != "windows":
					return "unsupported"
				default:
					return "on (run notify setup)"
				}
			},
			set: func(m *Model, _ string) {
				m.cfg.Notification.Desktop.Enabled = !m.cfg.Notification.Desktop.Enabled
				m.configChanged = true
			},
			isBool: true,
		},
```

Add `"runtime"` and the `internal/notify` import to `dialog.go`.

`m.devMode` may not exist under that name — check how the Model learns it is a dev build (`grep -n "devMode\|IsDev" internal/tui/*.go cmd/quil/main.go`). If the Model does not know, pass the `notify.Options` in via `SetDesktopNotifier` and store them, since that setter already receives the scheme; do **not** re-derive the variant from a build flag inside `internal/tui`.

`notify.Registered` is called at render, once per frame the Settings dialog is open. On Windows it is a file stat plus a registry read — cheap, and only while the dialog is on screen. If profiling ever says otherwise, cache it at dialog open, not on the Model.

- [ ] **Step 4: Run the test to verify it passes**

Run: `./scripts/dev.sh test ./internal/tui/`
Expected: PASS.

- [ ] **Step 5: Verify the whole suite**

Run: `./scripts/dev.sh test` and `./scripts/dev.sh vet`
Expected: clean. Adding a Settings row shifts every row index below it — if a dialog fixture test fails, it is asserting on row positions and needs its expectation updated, not the row moved.

- [ ] **Step 6: Manual check in dev mode**

1. Launch `./scripts/quil-dev.ps1`, confirm `[dev]` in the status bar.
2. F1 → Settings. Confirm the `Desktop notifications` row is present and reads `on (run notify setup)` before setup, `on` after.
3. Toggle it off, park an agent while unfocused, confirm **no** toast.
4. Toggle it on, park again, confirm a toast — **without relaunching**.
5. Exit and reopen. Confirm the setting survived.

- [ ] **Step 7: Single final commit**

This is the **only** commit in the plan.

```bash
git status --short          # confirm nothing unrelated is staged
git add internal/notify internal/tui internal/config cmd/quil docs .claude/CLAUDE.md
git status --short          # re-read before committing
git commit -F - <<'EOF'
feat(notify): desktop toasts for pane attention with click-to-route

Raise a Windows toast when a pane parks for input or finishes a turn
while the terminal is unfocused, and route to that exact project, tab
and pane when the toast is clicked.

The trigger derives from the before/after pair already captured in
applyWorkTransition, so both edges come from one call site rather than
from each write of blockedSince and unseen. Withdrawal is a sweep
instead: the falling edge has no choke point, and the commonest clear
path — the user typing an answer — fires no hook at all.

Activation returns over a per-PID named pipe rather than through the
daemon, so a click on the local machine never crosses ssh in remote
mode and addresses the client that raised the toast.

Toggle from F1 -> Settings or the TOML config; enabled by default. The
row reports registration state rather than the flag, because enabled
without a Start Menu shortcut is the default on a fresh Windows install
and renders no toast. It never registers on the user's behalf.

Toast artifacts are namespaced per build variant. They are the first
thing quil writes that QUIL_HOME cannot redirect, so a dev instance
would otherwise overwrite production's registration.

Windows only; other platforms get a nil notifier and no branching at
any call site. Raising the terminal window on click is not included.
EOF
```

Use the heredoc form shown — this repo's release tooling parses conventional commits, and a PowerShell here-string mangles the subject line.

---

## Self-Review

**Spec coverage.** Every spec section maps to a task: package layout → Task 2/3/4 plus 9/10/11; consumer interface → Task 7; WinRT mechanism → Tasks 1 and 9; Windows prerequisites → Task 10; dev namespacing → Tasks 4 and 12; edge detection → Task 7; withdrawal sweep → Task 8; focus gating → Task 6; cooldown → Task 7; data flow → Tasks 7, 8, 11, 12; trust boundary → Tasks 3 and 7; security ceiling → Tasks 2, 11, 12; config → Task 5; single-authority rule → Tasks 7 and 13; Settings row → Task 13; commands → Task 12; error handling → Tasks 7, 8, 12; testing → every task; success criteria 1–8 → Task 12 Step 8, criteria 9–10 → Task 13 Step 6.

**Deliberate deviation from the spec**, flagged rather than silent: the spec puts sanitize + escape + bound together in `toastxml.go`. Sanitizing moved to `internal/tui` (Task 7) because `sanitizeRemoteText` is unexported there and the package's own rule is that sanitizing happens at render, never in state. Escaping and bounding stay in `toastxml.go`. Both halves remain Linux-testable, which is the property the spec's placement was protecting.

**Type consistency.** `Notification{Title, Body, Tag, Launch}` is used identically in Tasks 3, 7, 9. `desktopNotifier` names exactly `Notify` and `Withdraw`; `notify.Notifier` adds `Close`, which only `cmd/quil` calls. `ValidPaneID` is the single pane-id predicate in Tasks 2, 11 and 12. `Variant(dev bool) Options` is called identically in Tasks 4, 9, 10, 12 and 13. `Registered(opts) (bool, error)` has the same signature in Tasks 9, 10 and 13. Config is reached as `m.cfg.Notification.Desktop` in Tasks 7 and 13 and nowhere is it cached on the Model.

**Three things an implementer must verify rather than assume**, called out at their step: the mute field's real name on `PaneModel` (Task 7), the existing `internal/tui` test-fixture construction helper (Task 7), and how the Model learns it is a dev build (Task 13). All three are stated as "read the neighbouring file first" rather than guessed at here.

**Ordering constraint worth restating.** Task 13 must run after Task 10, because its tri-state row calls `notify.Registered`. Running it earlier yields a row that compiles and always reports `unsupported`, which would pass its own test on Linux and be wrong on the platform it exists for.
