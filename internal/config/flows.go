package config

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/artyomsv/quil/internal/flow"
	"github.com/artyomsv/quil/internal/ipc"
)

//go:embed flows.toml
var defaultFlows string

type FlowRole struct {
	Agent   string   `toml:"agent" json:"agent"`
	Toggles []string `toml:"toggles" json:"toggles"`
	// Model is passed to the agent's own model flag at spawn (--model for
	// Claude Code and OpenCode, -m for Codex). Empty keeps the agent's
	// default. Quil validates the charset only; which ids exist is the
	// agent's business and a wrong one fails loudly in the pane.
	Model     string `toml:"model,omitempty" json:"model,omitempty"`
	Prompt    string `toml:"prompt" json:"prompt"`
	FixPrompt string `toml:"fix_prompt,omitempty" json:"fix_prompt,omitempty"`
}

// flowModelShape bounds a model id to what every supported agent accepts on
// its command line: OpenCode ids carry a provider prefix (anthropic/claude-…),
// Claude aliases carry dots and hyphens (claude-3.5-…), and nothing legitimate
// starts with "-", which is what keeps the value from reading as a flag.
var flowModelShape = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,79}$`)

type Flows struct {
	MaxReviewRounds    int                    `toml:"max_review_rounds" json:"max_review_rounds"`
	StepTimeoutMinutes int                    `toml:"step_timeout_minutes" json:"step_timeout_minutes"`
	Roles              map[flow.Role]FlowRole `toml:"roles" json:"roles"`
}

func FlowsPath() string { return filepath.Join(QuilDir(), "flows.toml") }

func DefaultFlows() Flows {
	var f Flows
	if err := toml.Unmarshal([]byte(defaultFlows), &f); err != nil {
		panic("invalid embedded flows: " + err.Error())
	}
	if err := f.Validate(); err != nil {
		panic("invalid embedded flows: " + err.Error())
	}
	return f
}

func LoadFlows() (Flows, error) {
	data, err := os.ReadFile(FlowsPath())
	if os.IsNotExist(err) {
		return DefaultFlows(), nil
	}
	if err != nil {
		return Flows{}, fmt.Errorf("read flows: %w", err)
	}
	var f Flows
	if err := toml.Unmarshal(data, &f); err != nil {
		return f, fmt.Errorf("parse flows: %w", err)
	}
	return f, f.Validate()
}

func (f Flows) Validate() error {
	if f.MaxReviewRounds < 0 {
		return fmt.Errorf("max_review_rounds must be non-negative")
	}
	// Keep the minute-to-duration conversion within int64 nanoseconds.
	if f.StepTimeoutMinutes < 0 || f.StepTimeoutMinutes > 153722867 {
		return fmt.Errorf("step_timeout_minutes is out of range")
	}
	for _, role := range flow.Roles {
		r := f.Roles[role]
		if strings.TrimSpace(r.Prompt) == "" {
			return fmt.Errorf("%s prompt is empty", role)
		}
		if role == flow.Developer && strings.TrimSpace(r.FixPrompt) == "" {
			return fmt.Errorf("developer fix_prompt is empty")
		}
		if flow.UnsafePromptText(r.Prompt) || flow.UnsafePromptText(r.FixPrompt) {
			return fmt.Errorf("%s prompt contains terminal control characters", role)
		}
		if strings.TrimSpace(r.Agent) == "" {
			return fmt.Errorf("%s agent is empty", role)
		}
		if r.Model != "" && !flowModelShape.MatchString(r.Model) {
			return fmt.Errorf("%s model %q is not a valid model id", role, r.Model)
		}
	}
	return nil
}

func WriteFlows(f Flows) error {
	if err := f.Validate(); err != nil {
		return err
	}
	var b bytes.Buffer
	if err := toml.NewEncoder(&b).Encode(f); err != nil {
		return err
	}
	path := FlowsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".flows-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(b.Bytes()); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

var flowPlaceholder = regexp.MustCompile(`\{\{[^{}]+\}\}`)

func UnknownFlowPlaceholders(prompt string) []string {
	var out []string
	for _, key := range flowPlaceholder.FindAllString(prompt, -1) {
		switch key {
		case "{{feature}}", "{{plan}}", "{{pr}}", "{{review}}":
		default:
			out = append(out, key)
		}
	}
	return out
}

func (cfg Flows) Prompt(f flow.Flow) string {
	r := cfg.Roles[f.Stage.Role()]
	prompt := r.Prompt
	if f.Stage == flow.StageFix {
		prompt = r.FixPrompt
	}
	// One pass: a placeholder in untrusted result text must stay literal.
	prompt = strings.NewReplacer("{{feature}}", f.Feature, "{{plan}}", f.Results.Plan,
		"{{pr}}", f.Results.PR, "{{review}}", f.Results.Notes).Replace(prompt)
	key, example := f.Stage.RequiredKey(), "{}"
	if key == "" {
		key = "none"
	} else {
		example = fmt.Sprintf(`{"%s": "..."}`, key)
	}
	if f.Stage == flow.StageReview {
		example = `{"verdict": "approved or changes", "notes": "..."}`
	}
	return prompt + fmt.Sprintf("\n\nWhen you finish, call the quil MCP tool report_step with status=\"done\" and\nresult=%s. Required keys for this step: %s. If you cannot\nfinish, call report_step with status=\"blocked\" and result={\"question\": \"...\"}.\nDo not do the work of any other role. Do not start subagents to do it.\n", example, key)
}

func (f Flows) Wire() ipc.FlowConfig {
	out := ipc.FlowConfig{MaxReviewRounds: f.MaxReviewRounds, StepTimeoutMinutes: f.StepTimeoutMinutes, Roles: make(map[flow.Role]ipc.FlowRoleConfig, len(f.Roles))}
	for role, r := range f.Roles {
		out.Roles[role] = ipc.FlowRoleConfig{Agent: r.Agent, Toggles: append([]string(nil), r.Toggles...), Model: r.Model, Prompt: r.Prompt, FixPrompt: r.FixPrompt}
	}
	return out
}
func FlowsFromWire(f ipc.FlowConfig) Flows {
	out := Flows{MaxReviewRounds: f.MaxReviewRounds, StepTimeoutMinutes: f.StepTimeoutMinutes, Roles: make(map[flow.Role]FlowRole, len(f.Roles))}
	for role, r := range f.Roles {
		out.Roles[role] = FlowRole{Agent: r.Agent, Toggles: append([]string(nil), r.Toggles...), Model: r.Model, Prompt: r.Prompt, FixPrompt: r.FixPrompt}
	}
	return out
}
