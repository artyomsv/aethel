package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/flow"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/google/uuid"
)

type flowDialogState struct {
	dest, projectID, projectName string
	branch, feature              string
	// cwd is the repository the flow works in, typed or picked. repos is the
	// daemon's git discovery near the project root, offered on the same row
	// with ←/→; a scan that fails or is slow leaves the typed path alone.
	cwd       string
	repos     []string
	editor    *TextEditor
	editRole  flow.Role
	editFix   bool
	row       int
	cfg       config.Flows
	plugins   []ipc.PluginCatalogEntry
	requestID string
	pending   bool
	err       string
	focusTab  string
}

type flowReplyMsg struct{ msg *ipc.Message }
type flowRequestTimeoutMsg string

func (m Model) activeFlow() *flow.Flow {
	if tab := m.activeTabModel(); tab != nil {
		return tab.Flow
	}
	return nil
}

func (m Model) paneFlow(id string) *flow.Flow {
	for _, proj := range m.projects {
		for _, tab := range proj.tabs {
			if tab.Flow != nil {
				for _, pid := range tab.Flow.Panes {
					if pid == id {
						return tab.Flow
					}
				}
			}
		}
	}
	return nil
}

// flowPaneRoles maps every role pane in a workspace broadcast to its role, so
// the layout code can place the panes the daemon created for a flow.
func flowPaneRoles(state WorkspaceStateMsg) map[string]flow.Role {
	out := make(map[string]flow.Role)
	for _, f := range state.Flows {
		for role, id := range f.Panes {
			if id != "" {
				out[id] = role
			}
		}
	}
	return out
}

// splitForNewPane places a pane the daemon created into an existing tree.
//
// An ordinary pane stacks below the first leaf, as it always has. A flow's
// role panes arrive two at a time after the analyst's worktree is ready, and
// stacking them gave three full-width rows nobody can read a diff in. They
// go analyst | (developer / reviewer): the second pane splits the only leaf
// side by side, and every later one splits the LAST leaf top/bottom, so the
// left column keeps its height and the right column grows downward. The
// user can still drag the borders afterwards.
func splitForNewPane(tab *TabModel, leaves []*PaneModel, pane *PaneModel, role flow.Role) {
	target, dir := leaves[0].ID, SplitVertical
	if role != "" {
		if len(leaves) == 1 {
			dir = SplitHorizontal
		} else {
			target = leaves[len(leaves)-1].ID
		}
	}
	tab.Root.SplitLeaf(target, dir)
	tab.Root.FillPlaceholder(pane)
	tab.invalidateLeaves()
}

func flowTabLabel(tab *TabModel) string {
	label := sanitizeRemoteText(tab.Name)
	f := tab.Flow
	if f == nil {
		return label
	}
	if f.Stage == flow.StageReadyForYou {
		return label + " ✓ PR " + sanitizeRemoteText(f.Results.PR)
	}
	label += " · " + sanitizeRemoteText(string(f.Stage))
	if f.Round > 0 {
		label += fmt.Sprintf(" · round %d", f.Round+1)
	}
	if f.Paused {
		label += " ⏸"
	}
	return label
}

func (m *Model) adoptFlowState(state WorkspaceStateMsg) tea.Cmd {
	byTab := make(map[string]flow.Flow)
	for _, f := range state.Flows {
		byTab[f.TabID] = f
	}
	for _, proj := range m.projects {
		if proj.Dest != state.Dest {
			continue
		}
		for _, tab := range proj.tabs {
			old := tab.Flow
			tab.Flow = nil
			if f, ok := byTab[tab.ID]; ok {
				copy := f.Clone()
				tab.Flow = &copy
				role := f.Stage.Role()
				if role == "" && f.Stage == flow.StagePreparing {
					role = flow.Analyst
				}
				p, _, _ := m.findPaneAndTab(f.Panes[role])
				if p == nil {
					for _, candidate := range flow.Roles {
						if p, _, _ = m.findPaneAndTab(f.Panes[candidate]); p != nil {
							break
						}
					}
				}
				if p != nil && f.Paused {
					p.blockedSince = time.UnixMilli(f.UpdatedAt)
					p.blockedReason = f.PauseWhy
				}
			}
			if old != nil && old.Paused && (tab.Flow == nil || !tab.Flow.Paused) {
				for _, id := range old.Panes {
					if p, _, _ := m.findPaneAndTab(id); p != nil && p.blockedReason == old.PauseWhy {
						p.blockedSince = time.Time{}
						p.blockedReason = ""
					}
				}
			}
		}
	}
	return m.focusNewFlowTab()
}

func (m *Model) focusNewFlowTab() tea.Cmd {
	if m.flowUI.focusTab == "" {
		return nil
	}
	for pi, proj := range m.projects {
		if proj.Dest != m.flowUI.dest {
			continue
		}
		for ti, tab := range proj.tabs {
			if tab.ID != m.flowUI.focusTab {
				continue
			}
			projectCmd := m.switchProject(pi)
			tabCmd := m.switchTab(ti)
			m.finalizeTabPanes(tab)
			m.flowUI.focusTab = ""
			return tea.Batch(projectCmd, tabCmd)
		}
	}
	return nil
}

func (m *Model) flowAttention(msg paneEventMsg) {
	paneID, typ := msg.PaneID, msg.Type
	if typ != "flow_paused" && typ != "flow_ready" {
		return
	}
	p, proj, _ := m.findPaneAndTab(paneID)
	if p == nil {
		for _, candidate := range m.projects {
			for _, tab := range candidate.tabs {
				if tab.ID == msg.TabID && len(tab.Leaves()) > 0 {
					p, proj = tab.Leaves()[0], candidate
					break
				}
			}
		}
	}
	if p == nil {
		return
	}
	if typ == "flow_paused" {
		p.blockedReason = msg.Title
		if p.blockedSince.IsZero() {
			p.blockedSince = time.Now()
		}
	} else {
		p.unseen = true
		p.unseenFromSeed = false
	}
	// The workspace frame precedes this event and already carries the pause
	// marker. The event is the one toast edge; the frame itself never toasts.
	m.raiseAttentionToast(p, proj, false, false)
}

func newFlowEditor(text string, width, height int) *TextEditor {
	e := NewTextEditor(text, "", max(12, width-12), max(2, min(8, height-14)))
	e.Highlight, e.SoftWrap = HighlightPlain, true
	return e
}

func (m Model) openNewFlow() (tea.Model, tea.Cmd) {
	f := flowDialogState{dest: m.activeDest(), branch: "feat/feature"}
	if p := m.activeProjectModel(); p != nil {
		f.projectID, f.projectName, f.cwd = p.ID, p.Name, p.RootDir
	}
	f.editor = newFlowEditor("", dialogWidth, m.height)
	m.flowUI, m.dialog = f, dialogNewFlow
	// Ask the daemon, not the local disk, which repositories sit near the
	// project root: the daemon holds the disk the flow will run on.
	var scan tea.Cmd
	if m.client != nil && f.cwd != "" {
		scan = m.requestGitRepos(f.dest, f.cwd, "", repoScanFlow, "")
	}
	return m, tea.Batch(tea.ClearScreen, scan)
}

// applyGitReposFlow files the discovered repositories on the open New flow
// dialog. The typed path is never replaced: the list is an offer on the same
// row, and a scan answering after the user typed a path must not undo it.
func (m *Model) applyGitReposFlow(repos []string) tea.Cmd {
	if m.dialog != dialogNewFlow {
		return nil
	}
	m.flowUI.repos = repos
	return nil
}

// flowTextTarget names the single-line field the flow dialog is focused on:
// "branch", "repo", "model" (with its role), or "" for the multi-line editor
// and every row that takes no text. It is the ONE answer to "where does typed
// or pasted text go", so the key handler and the paste handler cannot disagree
// about it — they did, and a paste that found no field fell through to the
// pane behind the dialog and typed the clipboard into a live shell.
func (m Model) flowTextTarget() (kind string, role flow.Role) {
	f := m.flowUI
	if m.dialog == dialogNewFlow {
		switch f.row {
		case 1:
			return "branch", ""
		case 2:
			return "repo", ""
		}
		return "", ""
	}
	if m.dialog == dialogFlowSettings && f.editor == nil {
		rows := m.flowSettingRows()
		if f.row >= 0 && f.row < len(rows) && rows[f.row].kind == "model" {
			return "model", rows[f.row].role
		}
	}
	return "", ""
}

// flowInsertText appends text to the focused single-line field, reporting
// whether a field took it. Control bytes and newlines are dropped and a space
// is kept, because a repository path may hold one (`C:/Program Files/repo`) —
// the old single-rune key check spelled a space as "space" and silently
// dropped it. A model id may not, so spaces are dropped on that row alone
// rather than saved into a value the charset check would refuse later.
func (m *Model) flowInsertText(text string) bool {
	kind, role := m.flowTextTarget()
	text = sanitizeDialogInput(text)
	switch kind {
	case "branch":
		m.flowUI.branch += text
	case "repo":
		m.flowUI.cwd += text
	case "model":
		r := m.flowUI.cfg.Roles[role]
		r.Model += strings.ReplaceAll(text, " ", "")
		m.flowUI.cfg.Roles[role] = r
	default:
		return false
	}
	return true
}

// flowBackspace drops the last rune of the focused single-line field.
func (m *Model) flowBackspace() bool {
	kind, role := m.flowTextTarget()
	cut := func(s string) string {
		if r := []rune(s); len(r) > 0 {
			return string(r[:len(r)-1])
		}
		return s
	}
	switch kind {
	case "branch":
		m.flowUI.branch = cut(m.flowUI.branch)
	case "repo":
		m.flowUI.cwd = cut(m.flowUI.cwd)
	case "model":
		r := m.flowUI.cfg.Roles[role]
		r.Model = cut(r.Model)
		m.flowUI.cfg.Roles[role] = r
	default:
		return false
	}
	return true
}

// flowPaste routes clipboard text to the flow dialog's own input, reporting
// whether the dialog consumed it. The multi-line editor takes it when it is
// focused; otherwise the focused single-line field does. A flow dialog with
// neither still returns true: swallowing a paste on a list row is right, and
// falling through would send the clipboard to the pane behind the dialog.
func (m *Model) flowPaste(text string) bool {
	if m.dialog != dialogNewFlow && m.dialog != dialogFlowSettings {
		return false
	}
	if kind, _ := m.flowTextTarget(); kind != "" {
		m.flowInsertText(text)
		return true
	}
	if m.flowUI.editor != nil {
		m.flowUI.editor.InsertMultiLine(strings.ReplaceAll(text, "\r", ""))
	}
	return true
}

// flowCycleRepo moves the repository row through the discovered list. A typed
// path that is not in the list starts from its head.
func (f *flowDialogState) flowCycleRepo(delta int) {
	if len(f.repos) == 0 {
		return
	}
	idx := -1
	for i, r := range f.repos {
		if r == f.cwd {
			idx = i
			break
		}
	}
	if idx < 0 {
		idx = 0
		if delta < 0 {
			idx = len(f.repos) - 1
		}
	} else {
		idx = (idx + delta + len(f.repos)) % len(f.repos)
	}
	f.cwd = f.repos[idx]
}

func (m Model) openFlowSettings() (tea.Model, tea.Cmd) {
	m.flowUI = flowDialogState{dest: m.activeDest(), cfg: config.DefaultFlows()}
	m.dialog = dialogFlowSettings
	cmd := m.sendFlowRequest(ipc.MsgFlowConfigReq, struct{}{})
	if m.client != nil {
		msg, _ := ipc.NewMessage(ipc.MsgPluginCatalogReq, struct{}{})
		msg.ID = m.flowUI.requestID
		if err := m.sendForDestStrict(m.flowUI.dest, msg); err != nil {
			m.flowUI.err = "Cannot load flow agents: " + err.Error()
			m.flowUI.pending = false
			m.flowUI.requestID = "" // Ignore the config reply after the destination failed.
		}
	}
	return m, tea.Batch(tea.ClearScreen, cmd)
}

func (m *Model) sendFlowRequest(typ string, payload any) tea.Cmd {
	m.flowUI.requestID = "flow-ui-" + uuid.NewString()
	m.flowUI.pending, m.flowUI.err = true, ""
	msg, err := ipc.NewMessage(typ, payload)
	if err == nil {
		msg.ID = m.flowUI.requestID
		if m.client == nil {
			err = fmt.Errorf("daemon is not connected")
		} else {
			err = m.sendForDestStrict(m.flowUI.dest, msg)
		}
	}
	if err != nil {
		m.flowUI.pending = false
		m.flowUI.err = err.Error()
		return nil
	}
	id := m.flowUI.requestID
	return tea.Tick(15*time.Second, func(time.Time) tea.Msg { return flowRequestTimeoutMsg(id) })
}

func (m *Model) applyFlowReply(reply flowReplyMsg) tea.Cmd {
	msg := reply.msg
	if msg.ID != m.flowUI.requestID || msg.Origin != m.flowUI.dest {
		return nil
	}
	if msg.Type == ipc.MsgPluginCatalogResp {
		var p ipc.PluginCatalogRespPayload
		if err := msg.DecodePayload(&p); err != nil {
			m.flowUI.err = err.Error()
		} else {
			m.flowUI.plugins = p.Plugins
		}
		return nil
	}
	m.flowUI.pending = false
	switch msg.Type {
	case ipc.MsgFlowConfigResp, ipc.MsgSaveFlowConfigResp:
		var p ipc.FlowConfigRespPayload
		if err := msg.DecodePayload(&p); err != nil {
			m.flowUI.err = err.Error()
			return nil
		}
		m.flowUI.err = p.Error
		if p.Error != "" {
			return nil
		}
		m.flowUI.cfg = config.FlowsFromWire(p.Config)
		if msg.Type == ipc.MsgSaveFlowConfigResp {
			reload, _ := ipc.NewMessage(ipc.MsgReloadPlugins, nil)
			if err := m.sendForDestStrict(m.flowUI.dest, reload); err != nil {
				m.flowUI.err = "Settings saved; reload failed: " + err.Error()
				return nil
			}
			m.dialog, m.dialogCursor = dialogSettings, 0
			return tea.ClearScreen
		}
	case ipc.MsgStartFlowResp, ipc.MsgResumeFlowResp:
		var p ipc.StartFlowRespPayload
		if err := msg.DecodePayload(&p); err != nil {
			m.flowUI.err = err.Error()
			return nil
		}
		m.flowUI.err = p.Error
		if p.Error != "" {
			if msg.Type == ipc.MsgResumeFlowResp {
				m.setFlash(sanitizeRemoteText(p.Error))
				return m.flashCmd()
			}
			return nil
		}
		if msg.Type == ipc.MsgStartFlowResp {
			m.dialog = dialogNone
			m.flowUI.focusTab = p.TabID
			return tea.Batch(m.focusNewFlowTab(), tea.ClearScreen)
		}
	}
	return nil
}

func flowBranch(feature string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(feature) {
		if b.Len() >= 48 {
			break
		}
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else if b.Len() > 0 && unicode.IsSpace(r) {
			b.WriteByte('-')
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		slug = "feature"
	}
	return "feat/" + slug
}

type flowSettingRow struct {
	label, kind string
	role        flow.Role
	toggle      string
}

func (m Model) flowSettingRows() []flowSettingRow {
	rows := []flowSettingRow{{label: "Max review rounds", kind: "rounds"}, {label: "Step timeout (minutes)", kind: "timeout"}}
	for _, role := range flow.Roles {
		rows = append(rows, flowSettingRow{label: string(role) + " agent", kind: "agent", role: role}, flowSettingRow{label: string(role) + " model", kind: "model", role: role}, flowSettingRow{label: string(role) + " prompt", kind: "prompt", role: role})
		if role == flow.Developer {
			rows = append(rows, flowSettingRow{label: "developer fix prompt", kind: "fix", role: role})
		}
		for _, p := range m.flowUI.plugins {
			if p.Name != m.flowUI.cfg.Roles[role].Agent {
				continue
			}
			for _, toggle := range p.Toggles {
				rows = append(rows, flowSettingRow{label: "  " + toggle.Name, kind: "toggle", role: role, toggle: toggle.Name})
			}
		}
	}
	return rows
}

func (m Model) handleFlowDialogKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	f := &m.flowUI
	if key == "esc" {
		if m.dialog == dialogFlowSettings && f.editor != nil {
			f.editor = nil
			return m, tea.ClearScreen
		}
		settings := m.dialog == dialogFlowSettings
		m.dialog = dialogNone
		if settings {
			m.dialog = dialogSettings
		}
		f.requestID = ""
		return m, tea.ClearScreen
	}
	if f.pending {
		return m, nil
	}
	if m.dialog == dialogNewFlow {
		if key == "ctrl+s" {
			if f.editor == nil {
				return m, nil
			}
			if f.branch == "feat/feature" || f.branch == flowBranch(f.feature) {
				f.branch = flowBranch(f.editor.Content())
			}
			f.feature = f.editor.Content()
			if strings.TrimSpace(f.feature) == "" {
				f.err = "Enter a feature request."
				return m, nil
			}
			// An empty repository row means the project root; the daemon
			// resolves that itself, so nothing is guessed on this side.
			cmd := m.sendFlowRequest(ipc.MsgStartFlowReq, ipc.StartFlowReqPayload{Feature: f.feature, Branch: f.branch, ProjectID: f.projectID, CWD: strings.TrimSpace(f.cwd)})
			return m, cmd
		}
		// Rows: 0 feature, 1 branch, 2 repository.
		if key == "tab" || key == "shift+tab" {
			if f.editor != nil && f.row == 0 && (f.branch == "feat/feature" || f.branch == flowBranch(f.feature)) {
				f.branch = flowBranch(f.editor.Content())
				f.feature = f.editor.Content()
			}
			if key == "tab" {
				f.row = (f.row + 1) % 3
			} else {
				f.row = (f.row + 2) % 3
			}
			return m, nil
		}
		if f.row == 2 && (key == "left" || key == "right") {
			delta := 1
			if key == "left" {
				delta = -1
			}
			f.flowCycleRepo(delta)
			return m, nil
		}
		if f.row == 1 || f.row == 2 {
			if key == "backspace" {
				m.flowBackspace()
			} else {
				// msg.Text, never msg.String(): the latter spells a space as
				// "space", so a single-rune test dropped it and no path with a
				// space in it could be typed.
				m.flowInsertText(msg.Text)
			}
			return m, nil
		}
		if f.editor != nil {
			_, _, cmd := f.editor.HandleKey(key)
			return m, cmd
		}
		return m, nil
	}
	if f.editor != nil {
		if key == "ctrl+s" {
			r := f.cfg.Roles[f.editRole]
			if f.editFix {
				r.FixPrompt = f.editor.Content()
			} else {
				r.Prompt = f.editor.Content()
			}
			f.cfg.Roles[f.editRole], f.editor = r, nil
			return m, tea.ClearScreen
		}
		_, _, cmd := f.editor.HandleKey(key)
		return m, cmd
	}
	if key == "ctrl+s" {
		cmd := m.sendFlowRequest(ipc.MsgSaveFlowConfigReq, ipc.SaveFlowConfigReqPayload{Config: f.cfg.Wire()})
		return m, cmd
	}
	rows := m.flowSettingRows()
	f.row = max(0, min(f.row, len(rows)-1))
	// The model row is typed, not cycled: Quil does not know which ids an
	// agent accepts, so there is nothing to cycle through. Letters go into
	// the id here, so the j/k navigation aliases apply on every other row.
	// Ctrl+S and Esc are answered ABOVE this branch, so the row still saves
	// and still closes; only text keys are claimed here.
	onModel := rows[f.row].kind == "model"
	if onModel && key != "up" && key != "down" {
		if key == "backspace" {
			m.flowBackspace()
		} else {
			m.flowInsertText(msg.Text)
		}
		return m, nil
	}
	if key == "up" || (key == "k" && !onModel) {
		f.row = max(0, f.row-1)
		return m, nil
	}
	if key == "down" || (key == "j" && !onModel) {
		f.row = min(len(rows)-1, f.row+1)
		return m, nil
	}
	if key != "enter" && key != " " && key != "left" && key != "right" {
		return m, nil
	}
	row := rows[f.row]
	r := f.cfg.Roles[row.role]
	delta := 1
	if key == "left" {
		delta = -1
	}
	switch row.kind {
	case "rounds":
		f.cfg.MaxReviewRounds = max(0, f.cfg.MaxReviewRounds+delta)
	case "timeout":
		f.cfg.StepTimeoutMinutes = max(0, f.cfg.StepTimeoutMinutes+delta)
	case "agent":
		var names []string
		idx := -1
		for _, p := range f.plugins {
			if p.Category == "ai" && p.Available {
				if p.Name == r.Agent {
					idx = len(names)
				}
				names = append(names, p.Name)
			}
		}
		if len(names) > 0 {
			idx = (idx + delta + len(names)) % len(names)
			// The model goes with the toggles: an id belongs to ONE agent, and
			// carrying `sonnet` onto codex produces a configuration the charset
			// check accepts and the agent rejects at spawn.
			r.Agent, r.Toggles, r.Model = names[idx], nil, ""
			for _, p := range f.plugins {
				if p.Name == r.Agent {
					for _, toggle := range p.Toggles {
						if toggle.Default {
							r.Toggles = append(r.Toggles, toggle.Name)
						}
					}
				}
			}
		}
	case "prompt", "fix":
		text := r.Prompt
		if row.kind == "fix" {
			text = r.FixPrompt
		}
		f.editRole, f.editFix = row.role, row.kind == "fix"
		f.editor = newFlowEditor(text, dialogWidth, m.height)
	case "toggle":
		found := false
		var toggles []string
		groups := make(map[string]string)
		for _, p := range f.plugins {
			if p.Name == r.Agent {
				for _, toggle := range p.Toggles {
					groups[toggle.Name] = toggle.Group
				}
			}
		}
		for _, name := range r.Toggles {
			if name == row.toggle {
				found = true
			} else if groups[row.toggle] == "" || groups[name] != groups[row.toggle] {
				toggles = append(toggles, name)
			}
		}
		if !found {
			toggles = append(toggles, row.toggle)
		}
		r.Toggles = toggles
	}
	if row.role != "" {
		f.cfg.Roles[row.role] = r
	}
	if row.kind == "agent" {
		if toggle := flowMissingPermissionToggle(r, f.plugins); toggle != "" {
			for i, candidate := range m.flowSettingRows() {
				if candidate.role == row.role && candidate.toggle == toggle {
					f.row = i
					break
				}
			}
		}
	}
	return m, nil
}

// Shipped plugins intentionally have no default-on permission modes. Keep the
// choice explicit; the first Codex mode bypasses approvals and the sandbox.
func flowMissingPermissionToggle(role config.FlowRole, plugins []ipc.PluginCatalogEntry) string {
	for _, p := range plugins {
		if p.Name != role.Agent {
			continue
		}
		first := ""
		for _, toggle := range p.Toggles {
			if toggle.Group != "permission_mode" {
				continue
			}
			if first == "" {
				first = toggle.Name
			}
			for _, selected := range role.Toggles {
				if selected == toggle.Name {
					return ""
				}
			}
		}
		return first
	}
	return ""
}

func (m Model) renderFlowDialog() string {
	f := m.flowUI
	inner := dialogInnerWidth(m.width, dialogWidth)
	var b strings.Builder
	title := "Flows"
	if m.dialog == dialogNewFlow {
		title = "New flow"
	}
	b.WriteString(dialogTitle.Render(title) + "\n")
	if f.pending {
		b.WriteString("Waiting for daemon…\n")
	}
	if f.err != "" {
		b.WriteString(sanitizeRemoteText(f.err) + "\n")
	}
	if m.dialog == dialogNewFlow {
		b.WriteString("Project: " + sanitizeRemoteText(f.projectName) + "\nFeature:\n")
	} else if f.editor == nil {
		for _, role := range flow.Roles {
			if flowMissingPermissionToggle(f.cfg.Roles[role], f.plugins) != "" {
				b.WriteString("Select a permission mode for " + string(role) + " before saving.\n")
			}
		}
		rows := m.flowSettingRows()
		start, end := historyWindow(len(rows), f.row, 0, max(2, m.height-12))
		for i := start; i < end; i++ {
			r := rows[i]
			value := "edit…"
			switch r.kind {
			case "rounds":
				value = strconv.Itoa(f.cfg.MaxReviewRounds)
			case "timeout":
				value = strconv.Itoa(f.cfg.StepTimeoutMinutes)
			case "agent":
				value = f.cfg.Roles[r.role].Agent
			case "model":
				value = f.cfg.Roles[r.role].Model
				if value == "" {
					value = "(agent default — type an id)"
				}
			case "toggle":
				value = "off"
				for _, t := range f.cfg.Roles[r.role].Toggles {
					if t == r.toggle {
						value = "on"
					}
				}
			}
			cursor := "  "
			if i == f.row {
				cursor = "> "
			}
			b.WriteString(cursor + sanitizeRemoteText(r.label) + ": " + sanitizeRemoteText(value) + "\n")
		}
	}
	if f.editor != nil {
		// Resize a copy: View must not mutate the editor shared with Update.
		e := *f.editor
		e.ViewWidth, e.ViewHeight = inner, max(2, min(8, m.height-14))
		// Remote prompt text stays raw in state; sanitize only the render copy.
		e.Lines = append([]string(nil), e.Lines...)
		for i := range e.Lines {
			e.Lines[i] = sanitizeRemoteText(e.Lines[i])
		}
		b.WriteString(e.Render())
		if unknown := config.UnknownFlowPlaceholders(f.editor.Content()); len(unknown) > 0 {
			b.WriteString("Unknown placeholder: " + sanitizeRemoteText(strings.Join(unknown, ", ")) + "\n")
		}
	}
	if m.dialog == dialogNewFlow {
		mark := func(row int) string {
			if f.row == row {
				return "> "
			}
			return "  "
		}
		repoHint := ""
		if n := len(f.repos); n > 0 {
			repoHint = fmt.Sprintf("  (←→ pick from %d found)", n)
		}
		b.WriteString(mark(1) + "Branch: " + sanitizeRemoteText(f.branch) + "\n")
		b.WriteString(mark(2) + "Repository: " + sanitizeRemoteText(f.cwd) + repoHint + "\n")
		b.WriteString("Tab: feature / branch / repository · Ctrl+S: start · Esc: cancel")
	} else {
		b.WriteString("↑↓: select · Enter/←→: edit · type on a model row · Ctrl+S: save · Esc: back")
	}
	return b.String()
}
