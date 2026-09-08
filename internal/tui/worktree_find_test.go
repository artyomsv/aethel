package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/plugin"
)

// Finding a worktree in a long list: ordering, the type-to-search filter, and
// the row label. Written against a repository with 58 worktrees, where the
// row the user wanted sat 49th under a branch name that shared no word with
// its folder, and the folder name — the part they were scanning for — was the
// tail the row truncation cut off.

// worktreeKey builds the key message handleCreatePaneSetupKey dispatches on.
func worktreeKey(name string) tea.KeyPressMsg {
	switch name {
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	}
	r := []rune(name)
	return tea.KeyPressMsg{Code: r[0], Text: name}
}

// worktreePaths lists the row paths in order, skipping the two fixed rows so
// an assertion about ORDER is not also an assertion about them.
func worktreePaths(rows []worktreeRow) []string {
	var out []string
	for _, r := range rows {
		if r.path == "" || r.path == worktreeNewRowPath {
			continue
		}
		out = append(out, r.path)
	}
	return out
}

// ---- ordering ----

// Newest commit first. git lists linked worktrees in admin-directory order —
// alphabetical by folder — which puts the one being worked on today wherever
// its name happens to fall.
func TestWorktreeRows_NewestCommitFirst(t *testing.T) {
	m := worktreePickModel(t, []ipc.WorktreeInfo{
		{Path: "/repo", Branch: "master", Main: true, CommitTime: 999},
		{Path: "/w/a", Branch: "a", CommitTime: 100},
		{Path: "/w/b", Branch: "b", CommitTime: 300},
		{Path: "/w/c", Branch: "c", CommitTime: 200},
	})
	got := worktreePaths(m.worktreeRows())
	want := []string{"/w/b", "/w/c", "/w/a"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("rows = %v, want newest commit first %v", got, want)
	}
}

// A daemon too old to send dates leaves every CommitTime at zero, and the
// list must then keep git's own order rather than shuffle on a tie.
func TestWorktreeRows_NoDatesKeepListingOrder(t *testing.T) {
	m := worktreePickModel(t, []ipc.WorktreeInfo{
		{Path: "/repo", Branch: "master", Main: true},
		{Path: "/w/c", Branch: "c"},
		{Path: "/w/a", Branch: "a"},
		{Path: "/w/b", Branch: "b"},
	})
	got := worktreePaths(m.worktreeRows())
	want := []string{"/w/c", "/w/a", "/w/b"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("rows = %v, want listing order %v", got, want)
	}
}

// The worktree the pane being SPLIT sits in goes first, whatever its commit
// date, and says so. That is the one the user is most often reaching for — a
// second agent beside the first, on the same checkout — and it was the row
// they could not find.
func TestWorktreeRows_CurrentWorktreeFirstAndTagged(t *testing.T) {
	m := newBranchModel(t)
	m.curTabs()[0].ActivePaneModel().CWD = "/repo-worktrees/feat-a/internal/tui"
	m.worktrees.list = []ipc.WorktreeInfo{
		{Path: "/repo", Branch: "master", Main: true, CommitTime: 900},
		{Path: "/repo-worktrees/feat-b", Branch: "feat-b", CommitTime: 500},
		{Path: "/repo-worktrees/feat-a", Branch: "feat-a", CommitTime: 100},
	}
	rows := m.worktreeRows()
	got := worktreePaths(rows)
	if len(got) == 0 || got[0] != "/repo-worktrees/feat-a" {
		t.Fatalf("rows = %v, want the pane's own worktree first", got)
	}
	cur := rows[worktreeRowIndex(t, m, "/repo-worktrees/feat-a")]
	if !strings.Contains(cur.label, "(current)") {
		t.Errorf("label = %q, want it tagged (current)", cur.label)
	}
	other := rows[worktreeRowIndex(t, m, "/repo-worktrees/feat-b")]
	if strings.Contains(other.label, "(current)") {
		t.Errorf("label = %q on another worktree carries the tag", other.label)
	}
}

// The pane's CWD is whatever OSC 7 reported and the worktree path is what git
// printed; on Windows those differ in separators and can differ in case.
func TestWorktreeRows_CurrentMatchesAcrossSeparatorsAndCase(t *testing.T) {
	m := newBranchModel(t)
	m.curTabs()[0].ActivePaneModel().CWD = `e:\Projects\Stukans\monorepo-worktrees\Fix-Verification-Service`
	m.worktrees.list = []ipc.WorktreeInfo{
		{Path: "E:/Projects/Stukans/monorepo", Branch: "master", Main: true},
		{Path: "E:/Projects/Stukans/monorepo-worktrees/other", Branch: "other", CommitTime: 500},
		{Path: "E:/Projects/Stukans/monorepo-worktrees/fix-verification-service", Branch: "feature/verification-996-followups"},
	}
	got := worktreePaths(m.worktreeRows())
	if got[0] != "E:/Projects/Stukans/monorepo-worktrees/fix-verification-service" {
		t.Errorf("rows = %v, want the pane's worktree first despite separator and case differences", got)
	}
}

// Containment is on a separator boundary: a pane in feat-ab is not in feat-a.
func TestWorktreeRows_CurrentNeedsASeparatorBoundary(t *testing.T) {
	m := newBranchModel(t)
	m.curTabs()[0].ActivePaneModel().CWD = "/repo-worktrees/feat-ab"
	m.worktrees.list = []ipc.WorktreeInfo{
		{Path: "/repo", Branch: "master", Main: true},
		{Path: "/repo-worktrees/feat-a", Branch: "feat-a"},
	}
	rows := m.worktreeRows()
	if r := rows[worktreeRowIndex(t, m, "/repo-worktrees/feat-a")]; strings.Contains(r.label, "(current)") {
		t.Errorf("label = %q — feat-ab was read as inside feat-a", r.label)
	}
}

// ---- searching ----

func filterModel(t *testing.T) Model {
	t.Helper()
	return worktreePickModel(t, []ipc.WorktreeInfo{
		{Path: "/repo", Branch: "master", Main: true},
		{Path: "/w/fix-verification-service", Branch: "feature/verification-996-followups", CommitTime: 300},
		{Path: "/w/invoicing-ui", Branch: "feat/invoicing-ui-rework", CommitTime: 200},
		{Path: "/w/tenant", Branch: "fix/dead-tenant", CommitTime: 100},
	})
}

func typeWorktree(t *testing.T, m Model, keys ...string) Model {
	t.Helper()
	p := &plugin.PanePlugin{}
	for _, k := range keys {
		updated, _ := m.handleSetupWorktreeKey(p, k)
		m = updated.(Model)
	}
	return m
}

// Typing narrows the list to the rows that contain the text. The two fixed
// rows go too: a search is a search for a WORKTREE, and a cursor that starts
// on "off" is two keystrokes from every result.
func TestWorktreeSearch_TypingNarrowsTheList(t *testing.T) {
	m := typeWorktree(t, filterModel(t), "t", "e", "n")
	if m.worktreeFilter != "ten" {
		t.Fatalf("filter = %q, want ten", m.worktreeFilter)
	}
	rows := m.worktreeRows()
	if len(rows) != 1 || rows[0].path != "/w/tenant" {
		t.Errorf("rows = %+v, want only the tenant worktree", rows)
	}
	if m.worktreeCursor != 0 {
		t.Errorf("cursor = %d, want 0 on the first match", m.worktreeCursor)
	}
}

// The FOLDER name matches, not only the branch. The user scans for the name
// they see in the pane header and on disk; the branch can share no word with
// it.
func TestWorktreeSearch_MatchesTheFolderName(t *testing.T) {
	m := typeWorktree(t, filterModel(t), "s", "e", "r", "v", "i", "c", "e")
	rows := m.worktreeRows()
	if len(rows) != 1 || rows[0].path != "/w/fix-verification-service" {
		t.Errorf("rows = %+v, want the worktree whose folder holds %q", rows, "service")
	}
}

func TestWorktreeSearch_IgnoresCase(t *testing.T) {
	m := typeWorktree(t, filterModel(t), "T", "E", "N")
	rows := m.worktreeRows()
	if len(rows) != 1 || rows[0].path != "/w/tenant" {
		t.Errorf("rows = %+v, want a case-insensitive match", rows)
	}
}

// j and k are LETTERS now, the same trade the name field makes: a list that
// can be typed into cannot also move on two of the keys a name contains.
func TestWorktreeSearch_JAndKTypeRatherThanMove(t *testing.T) {
	m := typeWorktree(t, filterModel(t), "j")
	if m.worktreeFilter != "j" {
		t.Errorf("filter = %q, want j — the key was taken as cursor movement", m.worktreeFilter)
	}
}

func TestWorktreeSearch_BackspaceDropsOneRune(t *testing.T) {
	m := typeWorktree(t, filterModel(t), "t", "é", "backspace")
	if m.worktreeFilter != "t" {
		t.Errorf("filter = %q after backspace, want t", m.worktreeFilter)
	}
}

// Esc clears the search FIRST; only an Esc on an empty search backs out of the
// dialog. Routed through the dialog's key handler because that is where the
// shared Esc branch returns unconditionally — the same trap the name field's
// Esc fell into.
func TestWorktreeSearch_EscClearsBeforeClosingTheDialog(t *testing.T) {
	m := typeWorktree(t, filterModel(t), "t", "e", "n")

	updated, _ := m.handleCreatePaneSetupKey(worktreeKey("esc"))
	m = updated.(Model)
	if m.worktreeFilter != "" {
		t.Fatalf("filter = %q after Esc, want cleared", m.worktreeFilter)
	}
	if m.dialog != dialogCreatePaneSetup {
		t.Fatal("the first Esc closed the dialog instead of clearing the search")
	}

	updated, _ = m.handleCreatePaneSetupKey(worktreeKey("esc"))
	if got := updated.(Model); got.dialog == dialogCreatePaneSetup {
		t.Error("the second Esc did not back out of the dialog")
	}
}

// Enter on a match commits it, drops the search, and leaves the cursor on the
// chosen row of the FULL list — so the user sees where their choice sits.
func TestWorktreeSearch_EnterCommitsAndClearsTheSearch(t *testing.T) {
	m := typeWorktree(t, filterModel(t), "t", "e", "n", "enter")
	if m.selectedWorktree != "/w/tenant" {
		t.Fatalf("selectedWorktree = %q, want /w/tenant", m.selectedWorktree)
	}
	if m.worktreeFilter != "" {
		t.Errorf("filter = %q after Enter, want cleared", m.worktreeFilter)
	}
	rows := m.worktreeRows()
	if len(rows) < 5 {
		t.Fatalf("rows = %+v, want the full list back", rows)
	}
	if rows[m.worktreeCursor].path != "/w/tenant" {
		t.Errorf("cursor on %q, want the chosen row", rows[m.worktreeCursor].path)
	}
}

// A search that matches nothing must still take keys: the way out is
// backspace, and a handler that goes inert on an empty list swallows it.
func TestWorktreeSearch_NoMatchStillAcceptsBackspace(t *testing.T) {
	m := typeWorktree(t, filterModel(t), "z", "z", "q")
	if len(m.worktreeRows()) != 0 {
		t.Fatalf("rows = %+v, want none for zzq", m.worktreeRows())
	}
	m = typeWorktree(t, m, "enter") // nothing to commit; must not panic
	m = typeWorktree(t, m, "backspace")
	if m.worktreeFilter != "zz" {
		t.Errorf("filter = %q, want zz — backspace was swallowed on an empty match", m.worktreeFilter)
	}
}

// Bounded at ingest, like every other typed field that is rendered per frame.
func TestWorktreeSearch_IsBounded(t *testing.T) {
	m := filterModel(t)
	for i := 0; i < worktreeFilterCap+10; i++ {
		m = typeWorktree(t, m, "a")
	}
	if n := len([]rune(m.worktreeFilter)); n != worktreeFilterCap {
		t.Errorf("filter holds %d runes, want the cap %d", n, worktreeFilterCap)
	}
}

// ---- resets ----

// The search belongs to the listing it narrowed. A new directory drops the
// listing, so it drops the search with it.
func TestWorktreeSearch_ClearedWhenTheDirectoryChanges(t *testing.T) {
	m := typeWorktree(t, filterModel(t), "t")
	m.onSetupCWDChanged("/other")
	if m.worktreeFilter != "" {
		t.Errorf("filter = %q after a directory change, want cleared", m.worktreeFilter)
	}
}

// And the dialog teardown after a submit, or the next Ctrl+N opens narrowed
// to a word typed for a different pane.
func TestWorktreeSearch_ClearedByTheDialogTeardown(t *testing.T) {
	m := newBranchModel(t)
	m.client = &fakeSender{}
	m.selectedPlugin = "terminal"
	m.selectedCWD = m.cwdBrowseDir
	m.worktreeFilter = "feat"
	m.dialogCursor = 0

	updated, _ := m.handleCreatePaneSplit()
	if got := updated.(Model); got.worktreeFilter != "" {
		t.Errorf("filter = %q after submit, want cleared", got.worktreeFilter)
	}
}

// ---- rendering ----

// The search sits on the field's label row, so it costs no list row and
// worktreeVisibleRows' budget is untouched. Empty, the row says the list can
// be typed into; nothing on screen said so before.
func TestRenderSetup_WorktreeSearchShowsOnTheLabelRow(t *testing.T) {
	m := filterModel(t)
	empty := stripANSI(m.renderCreatePaneSetupDialog())
	if !strings.Contains(empty, "Worktree    type to search") {
		t.Errorf("focused field with no search does not invite typing\n%s", empty)
	}

	m = typeWorktree(t, m, "t", "e", "n")
	out := stripANSI(m.renderCreatePaneSetupDialog())
	if !strings.Contains(out, "Worktree    search: ten") {
		t.Errorf("search text is not on the label row\n%s", out)
	}
	if strings.Contains(out, "type to search") {
		t.Errorf("the hint is still shown beside a live search\n%s", out)
	}
}

func TestRenderSetup_WorktreeSearchWithNoMatchSaysSo(t *testing.T) {
	m := typeWorktree(t, filterModel(t), "z", "z", "q")
	out := stripANSI(m.renderCreatePaneSetupDialog())
	if !strings.Contains(out, "no worktree matches") {
		t.Errorf("an empty match rendered as a blank list\n%s", out)
	}
}

// The search text is the user's own typing, but it is rendered through the
// same sanitizer as every other value on the row — a C1 CSI introducer or a
// bidi override is printable, and this is the one draw site of the value.
func TestRenderSetup_WorktreeSearchIsSanitized(t *testing.T) {
	m := typeWorktree(t, filterModel(t), "t", "\u202e")
	out := m.renderCreatePaneSetupDialog()
	if strings.Contains(out, "\u202e") {
		t.Error("rendered output carries a bidi override from the search text")
	}
}

// A long row keeps the FOLDER name: the path is cut at its head, never its
// tail, because the head is the same for every row and the tail is the one
// part the user recognises.
func TestRenderSetup_WorktreeRowKeepsTheFolderNameVisible(t *testing.T) {
	m := worktreePickModel(t, []ipc.WorktreeInfo{
		{Path: "/repo", Main: true, Branch: "master"},
		{Path: "E:/Projects/Stukans/some/deeply/nested/place/monorepo-worktrees/fix-verification-service", Branch: "feat/verify"},
	})
	out := stripANSI(m.renderCreatePaneSetupDialog())
	if !strings.Contains(out, "fix-verification-service") {
		t.Errorf("the folder name was cut off the row\n%s", out)
	}
	if !strings.Contains(out, "feat/verify") {
		t.Errorf("the branch was lost from the row\n%s", out)
	}
	if !strings.Contains(out, "…") {
		t.Errorf("a row wider than the dialog was not elided\n%s", out)
	}
}
