package flow

import (
	"strings"
	"testing"
)

func TestNext(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stage  Stage
		report *Report
		want   Stage
		why    string
	}{
		{"plan", StagePlan, &Report{"done", map[string]string{"plan": "tickets"}}, StageBuild, ""},
		{"build", StageBuild, &Report{"done", map[string]string{"pr": "42"}}, StageReview, ""},
		{"approve", StageReview, &Report{"done", map[string]string{"verdict": "approved"}}, StageReadyForYou, ""},
		{"changes", StageReview, &Report{"done", map[string]string{"verdict": "changes"}}, StageFix, ""},
		{"fix", StageFix, &Report{"done", nil}, StageReview, ""},
		{"no plan", StagePlan, &Report{"done", nil}, StagePlan, "step reported no plan"},
		{"no pr", StageBuild, &Report{"done", nil}, StageBuild, "step reported no pr"},
		{"no verdict", StageReview, &Report{"done", nil}, StageReview, "step reported no verdict"},
		{"bad verdict", StageReview, &Report{"done", map[string]string{"verdict": "maybe"}}, StageReview, "review verdict must be approved or changes"},
		{"blocked", StageBuild, &Report{"blocked", map[string]string{"question": "Which API?"}}, StageBuild, "Which API?"},
		{"empty question", StageFix, &Report{"blocked", nil}, StageFix, "step reported no question"},
		{"no report", StagePlan, nil, StagePlan, "agent stopped without reporting"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := Flow{Stage: tc.stage, Round: 1, MaxReviewRounds: 3, TaskID: "old"}
			got, err := Next(f, tc.report)
			if err != nil || got.Stage != tc.want || got.PauseWhy != tc.why || got.Paused != (tc.why != "") || got.TaskID != "" {
				t.Fatalf("got %+v, %v", got, err)
			}
			if got.Paused {
				resumed, err := Resume(got)
				if err != nil || resumed.Paused || resumed.PauseWhy != "" || resumed.Stage != f.Stage || resumed.Round != f.Round {
					t.Fatalf("resume %+v, %v", resumed, err)
				}
			}
		})
	}
}

func TestWholeEpicAndRoundLimit(t *testing.T) {
	f := Flow{Stage: StagePreparing, MaxReviewRounds: 1, Panes: map[Role]string{Analyst: "a", Developer: "d", Reviewer: "r"}}
	step := func(r *Report, want Stage) {
		t.Helper()
		var err error
		f, err = Next(f, r)
		if err != nil || f.Stage != want {
			t.Fatalf("%+v %v", f, err)
		}
	}
	step(nil, StagePlan)
	step(&Report{"done", map[string]string{"plan": "epic"}}, StageBuild)
	step(&Report{"done", map[string]string{"pr": "17"}}, StageReview)
	step(&Report{"done", map[string]string{"verdict": "changes", "notes": "add tests"}}, StageFix)
	if f.Round != 1 || f.Results.Notes != "add tests" {
		t.Fatal(f)
	}
	step(&Report{"done", nil}, StageReview)
	step(&Report{"done", map[string]string{"verdict": "changes"}}, StageReview)
	if !f.Paused || f.PauseWhy != "review round limit reached" || f.Round != 1 {
		t.Fatal(f)
	}
	f, _ = Resume(f)
	step(&Report{"done", map[string]string{"verdict": "approved"}}, StageReadyForYou)
	if f.Results.PR != "17" || f.Results.Plan != "epic" {
		t.Fatal(f)
	}
	if _, err := Next(f, nil); err == nil {
		t.Fatal("terminal stage advanced")
	}
}

func TestGuardsAndResumeFailures(t *testing.T) {
	if _, err := Next(Flow{Stage: StagePreparing}, nil); err == nil {
		t.Fatal("missing panes accepted")
	}
	if _, err := Resume(Flow{Stage: StagePlan}); err == nil {
		t.Fatal("running flow resumed")
	}
	for _, why := range []string{"process exited", "timed out", "daemon restarted", "pane developer was closed", "queue full"} {
		f := Pause(Flow{Stage: StageBuild, Round: 2, TaskID: "t"}, why)
		if _, err := Next(f, nil); err == nil {
			t.Fatal("paused flow advanced")
		}
		f, err := Resume(f)
		if err != nil || f.Stage != StageBuild || f.Round != 2 || f.Paused || f.TaskID != "" {
			t.Fatal(f, err)
		}
	}
}

func TestReportBounds(t *testing.T) {
	for _, r := range []Report{{"invalid", nil}, {"done", map[string]string{"plan": strings.Repeat("x", 8193)}}} {
		if ValidateReport(r) == nil {
			t.Fatal("invalid report accepted")
		}
	}
	r := Report{"done", map[string]string{}}
	for i := 0; i < 17; i++ {
		r.Result[string(rune('a'+i))] = "x"
	}
	if ValidateReport(r) == nil {
		t.Fatal("17 values accepted")
	}
}
