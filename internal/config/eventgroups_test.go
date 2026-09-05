package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/artyomsv/quil/internal/config"
)

func TestDefault_EventGroups(t *testing.T) {
	got := config.Default().Notification.Events
	want := config.EventGroupsConfig{
		AgentTurn:     true,
		AgentBlocked:  true,
		AgentSubagent: true,
		AgentSession:  true,
		Process:       true,
		Pane:          true,
		MCP:           true,
		System:        true,
		Commands:      false,
		Idle:          false,
	}
	if got != want {
		t.Errorf("Default event groups:\n got %+v\nwant %+v", got, want)
	}
}

// A config.toml written before this feature existed has no [notification.events]
// section. Load starts from Default() and decodes over it, so every group must
// survive untouched — otherwise an upgrade blanks the sidebar.
func TestLoad_NoEventsSection_KeepsDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[notification]\nsidebar_width = 40\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Notification.SidebarWidth != 40 {
		t.Errorf("sidebar_width: got %d, want 40", cfg.Notification.SidebarWidth)
	}
	if cfg.Notification.Events != config.Default().Notification.Events {
		t.Errorf("event groups: got %+v, want the defaults", cfg.Notification.Events)
	}
}

// A partial section must override only the keys it names.
func TestLoad_PartialEventsSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := "[notification.events]\nidle = true\nagent_turn = false\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	ev := cfg.Notification.Events
	if !ev.Idle {
		t.Error("idle: got false, want true (explicitly set)")
	}
	if ev.AgentTurn {
		t.Error("agent_turn: got true, want false (explicitly set)")
	}
	if !ev.AgentBlocked || !ev.Process || !ev.System {
		t.Errorf("unnamed groups lost their defaults: %+v", ev)
	}
	if ev.Commands {
		t.Error("commands: got true, want false (default)")
	}
}

// Save serialises the whole struct, so a round trip must preserve every group.
func TestSaveLoad_EventGroupsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	in := config.Default()
	in.Notification.Events.Idle = true
	in.Notification.Events.AgentSubagent = false
	if err := config.Save(path, in); err != nil {
		t.Fatalf("Save: %v", err)
	}

	out, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out.Notification.Events != in.Notification.Events {
		t.Errorf("round trip: got %+v, want %+v", out.Notification.Events, in.Notification.Events)
	}
}
