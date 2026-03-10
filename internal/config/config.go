package config

import (
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Daemon      DaemonConfig      `toml:"daemon"`
	GhostBuffer GhostBufferConfig `toml:"ghost_buffer"`
	Logging     LoggingConfig     `toml:"logging"`
	Security    SecurityConfig    `toml:"security"`
	UI          UIConfig          `toml:"ui"`
	Keybindings KeybindingsConfig `toml:"keybindings"`
}

type DaemonConfig struct {
	SnapshotInterval string `toml:"snapshot_interval"`
	AutoStart        bool   `toml:"auto_start"`
}

type GhostBufferConfig struct {
	MaxLines int  `toml:"max_lines"`
	Dimmed   bool `toml:"dimmed"`
}

type LoggingConfig struct {
	Level     string `toml:"level"`
	MaxSizeMB int    `toml:"max_size_mb"`
	MaxFiles  int    `toml:"max_files"`
}

type SecurityConfig struct {
	EncryptTokens bool `toml:"encrypt_tokens"`
	RedactSecrets bool `toml:"redact_secrets"`
}

type UIConfig struct {
	TabDock string `toml:"tab_dock"`
	Theme   string `toml:"theme"`
}

type KeybindingsConfig struct {
	SplitHorizontal string `toml:"split_horizontal"`
	SplitVertical   string `toml:"split_vertical"`
	NextPane        string `toml:"next_pane"`
	PrevPane        string `toml:"prev_pane"`
	NewTab          string `toml:"new_tab"`
	ClosePane       string `toml:"close_pane"`
	JSONTransform   string `toml:"json_transform"`
	QuickActions    string `toml:"quick_actions"`
}

func Default() Config {
	return Config{
		Daemon: DaemonConfig{
			SnapshotInterval: "30s",
			AutoStart:        true,
		},
		GhostBuffer: GhostBufferConfig{
			MaxLines: 500,
			Dimmed:   true,
		},
		Logging: LoggingConfig{
			Level:     "info",
			MaxSizeMB: 10,
			MaxFiles:  3,
		},
		Security: SecurityConfig{
			EncryptTokens: true,
			RedactSecrets: true,
		},
		UI: UIConfig{
			TabDock: "top",
			Theme:   "default",
		},
		Keybindings: KeybindingsConfig{
			SplitHorizontal: "ctrl+shift+h",
			SplitVertical:   "ctrl+shift+v",
			NextPane:        "ctrl+tab",
			PrevPane:        "ctrl+shift+tab",
			NewTab:          "ctrl+t",
			ClosePane:       "ctrl+w",
			JSONTransform:   "ctrl+j",
			QuickActions:    "ctrl+a",
		},
	}
}

func Load(path string) (Config, error) {
	cfg := Default()
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func AethelDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".aethel")
}

func ConfigPath() string {
	return filepath.Join(AethelDir(), "config.toml")
}

func SocketPath() string {
	return filepath.Join(AethelDir(), "aetheld.sock")
}
