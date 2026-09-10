// Package flow is the built-in epic workflow. It has no runtime dependencies.
package flow

import (
	"fmt"
	"regexp"
	"strings"
)

type Stage string
type Role string

const (
	StagePreparing   Stage = "preparing"
	StagePlan        Stage = "plan"
	StageBuild       Stage = "build"
	StageReview      Stage = "review"
	StageFix         Stage = "fix"
	StageReadyForYou Stage = "ready_for_you"
	Analyst          Role  = "analyst"
	Developer        Role  = "developer"
	Reviewer         Role  = "reviewer"
)

var Roles = [...]Role{Analyst, Developer, Reviewer}

type Flow struct {
	ID              string          `json:"id"`
	TabID           string          `json:"tab_id"`
	Branch          string          `json:"branch"`
	Feature         string          `json:"feature"`
	Stage           Stage           `json:"stage"`
	Paused          bool            `json:"paused"`
	PauseWhy        string          `json:"pause_why,omitempty"`
	Round           int             `json:"round"`
	Panes           map[Role]string `json:"panes"`
	Results         Results         `json:"results"`
	TaskID          string          `json:"-"`
	CreatedAt       int64           `json:"created_at"`
	UpdatedAt       int64           `json:"updated_at"`
	MaxReviewRounds int             `json:"max_review_rounds"`
}

type Results struct {
	Plan    string `json:"plan,omitempty"`
	PR      string `json:"pr,omitempty"`
	Verdict string `json:"verdict,omitempty"`
	Notes   string `json:"notes,omitempty"`
}

type Report struct {
	Status string            `json:"status"`
	Result map[string]string `json:"result"`
}

func (s Stage) Role() Role {
	switch s {
	case StagePlan:
		return Analyst
	case StageBuild, StageFix:
		return Developer
	case StageReview:
		return Reviewer
	}
	return ""
}

func (s Stage) RequiredKey() string {
	switch s {
	case StagePlan:
		return "plan"
	case StageBuild:
		return "pr"
	case StageReview:
		return "verdict"
	}
	return ""
}

func (f Flow) Clone() Flow {
	f.Panes = clonePanes(f.Panes)
	return f
}

func clonePanes(src map[Role]string) map[Role]string {
	out := make(map[Role]string, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func Pause(f Flow, why string) Flow {
	f.Paused, f.PauseWhy, f.TaskID = true, why, ""
	return f
}

func Resume(f Flow) (Flow, error) {
	if !f.Paused {
		return f, fmt.Errorf("flow is not paused")
	}
	f.Paused, f.PauseWhy, f.TaskID = false, "", ""
	return f, nil
}

// ValidateReport checks the wire envelope; stage-specific validation happens
// only when the task ends, so an agent can correct a report before going idle.
// UnsafePromptText rejects terminal controls while preserving multiline text.
func UnsafePromptText(s string) bool {
	return strings.ContainsAny(s, "\x1b\u009b\r") || strings.Contains(s, string([]byte{0x9b}))
}

var prShape = regexp.MustCompile(`^([0-9]{1,10}|[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+#[0-9]{1,10}|https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+/pull/[0-9]{1,10})$`)

func ValidateReport(r Report) error {
	if r.Status != "done" && r.Status != "blocked" {
		return fmt.Errorf("status must be done or blocked")
	}
	if len(r.Result) > 16 {
		return fmt.Errorf("result has more than 16 values")
	}
	for k, v := range r.Result {
		if UnsafePromptText(k) || UnsafePromptText(v) {
			return fmt.Errorf("result values may not contain terminal control characters")
		}
		if len(k) > 8192 || len(v) > 8192 {
			return fmt.Errorf("result values must be at most 8 KiB")
		}
	}
	return nil
}

// Next consumes a settled step. A nil report is an unreported idle edge.
// Pauses are valid outcomes; errors mean the caller requested an illegal edge.
func Next(f Flow, report *Report) (Flow, error) {
	if f.Paused {
		return f, fmt.Errorf("flow is paused")
	}
	f.TaskID = ""
	if f.Stage == StagePreparing {
		for _, role := range Roles {
			if f.Panes[role] == "" {
				return f, fmt.Errorf("missing %s pane", role)
			}
		}
		f.Stage = StagePlan
		return f, nil
	}
	if f.Stage.Role() == "" {
		return f, fmt.Errorf("stage %s has no step", f.Stage)
	}
	if report == nil {
		return Pause(f, "agent stopped without reporting"), nil
	}
	if err := ValidateReport(*report); err != nil {
		return Pause(f, err.Error()), nil
	}
	if report.Status == "blocked" {
		why := report.Result["question"]
		if strings.TrimSpace(why) == "" {
			why = "step reported no question"
		}
		return Pause(f, why), nil
	}
	key := f.Stage.RequiredKey()
	if key != "" && strings.TrimSpace(report.Result[key]) == "" {
		return Pause(f, "step reported no "+key), nil
	}
	switch f.Stage {
	case StagePlan:
		f.Results.Plan, f.Stage = report.Result["plan"], StageBuild
	case StageBuild:
		pr := strings.TrimSpace(report.Result["pr"])
		if !prShape.MatchString(pr) {
			return Pause(f, "pr must be a PR number, owner/repo#N, or GitHub PR URL"), nil
		}
		f.Results.PR, f.Stage = pr, StageReview
	case StageReview:
		verdict := report.Result["verdict"]
		if verdict != "approved" && verdict != "changes" {
			return Pause(f, "review verdict must be approved or changes"), nil
		}
		f.Results.Verdict, f.Results.Notes = verdict, report.Result["notes"]
		if verdict == "approved" {
			f.Stage = StageReadyForYou
			break
		}
		if f.Round+1 > f.MaxReviewRounds {
			return Pause(f, "review round limit reached"), nil
		}
		f.Round++
		f.Stage = StageFix
	case StageFix:
		f.Stage = StageReview
	}
	return f, nil
}
