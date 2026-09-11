package config

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/flow"
	"github.com/artyomsv/quil/internal/plugin"
)

func TestEmbeddedFlows_NoCarriageReturns(t *testing.T) {
	if strings.ContainsRune(defaultFlows, '\r') {
		t.Fatal("embedded flow prompts contain CR; check .gitattributes eol=lf")
	}
}

func TestFlows_Default_RoundTripsWithShippedToggles(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	f, err := LoadFlows()
	if err != nil || f.MaxReviewRounds != 3 {
		t.Fatal(f, err)
	}
	dir := t.TempDir()
	if _, err := plugin.EnsureDefaultPlugins(dir); err != nil {
		t.Fatal(err)
	}
	for _, role := range flow.Roles {
		r := f.Roles[role]
		// Read the shipped definitions instead of duplicating toggle names.
		data, err := os.ReadFile(dir + "/" + r.Agent + ".toml")
		if err != nil {
			t.Fatal(err)
		}
		for _, toggle := range r.Toggles {
			if !strings.Contains(string(data), `name = "`+toggle+`"`) {
				t.Fatalf("%s missing %s", role, toggle)
			}
		}
	}
	f.MaxReviewRounds = 7
	if err := WriteFlows(f); err != nil {
		t.Fatal(err)
	}
	got, err := LoadFlows()
	if err != nil || !reflect.DeepEqual(got, f) {
		t.Fatalf("round trip: %+v %v", got, err)
	}
}

func TestFlowPrompt_Placeholders_PreservesFixedTail(t *testing.T) {
	f := DefaultFlows()
	r := f.Roles[flow.Analyst]
	r.Prompt = "{{feature}} / {{unknown}}"
	f.Roles[flow.Analyst] = r
	got := f.Prompt(flow.Flow{Stage: flow.StagePlan, Feature: "{{plan}}", Results: flow.Results{Plan: "must not appear"}})
	for _, want := range []string{"{{plan}} / {{unknown}}", "report_step", "Required keys for this step: plan", "Do not start subagents"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %s", want, got)
		}
	}
	if strings.Contains(got, "must not appear") {
		t.Fatal("recursively replaced untrusted result")
	}
	if got := UnknownFlowPlaceholders(r.Prompt); !reflect.DeepEqual(got, []string{"{{unknown}}"}) {
		t.Fatal(got)
	}
	if !strings.Contains(f.Prompt(flow.Flow{Stage: flow.StageFix}), "Required keys for this step: none") {
		t.Fatal("fix demands a key")
	}
}

func TestFlows_EmptyPrompts_RefusesSave(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	for _, role := range flow.Roles {
		f := DefaultFlows()
		r := f.Roles[role]
		r.Prompt = " \n\t"
		f.Roles[role] = r
		if err := WriteFlows(f); err == nil {
			t.Fatal("accepted empty prompt", role)
		}
	}
	f := DefaultFlows()
	r := f.Roles[flow.Developer]
	r.FixPrompt = ""
	f.Roles[flow.Developer] = r
	if err := WriteFlows(f); err == nil {
		t.Fatal("accepted empty fix prompt")
	}
}

func TestFlows_Model_ValidatesShapeAndRoundTrips(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	for _, role := range flow.Roles {
		if DefaultFlows().Roles[role].Model != "" {
			t.Fatal("shipped default pins a model for", role)
		}
	}
	f := DefaultFlows()
	r := f.Roles[flow.Developer]
	r.Model = "gpt-5.1-codex"
	f.Roles[flow.Developer] = r
	a := f.Roles[flow.Analyst]
	a.Model = "anthropic/claude-opus-5"
	f.Roles[flow.Analyst] = a
	if err := WriteFlows(f); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadFlows()
	if err != nil || loaded.Roles[flow.Developer].Model != "gpt-5.1-codex" || loaded.Roles[flow.Analyst].Model != "anthropic/claude-opus-5" {
		t.Fatal(loaded, err)
	}
	if wire := FlowsFromWire(f.Wire()); !reflect.DeepEqual(wire.Roles[flow.Developer], f.Roles[flow.Developer]) {
		t.Fatal("model lost on the wire", wire.Roles[flow.Developer])
	}
	for _, bad := range []string{"-m", "a b", "x\x1b[0m", "id;rm", strings.Repeat("a", 81)} {
		r.Model = bad
		f.Roles[flow.Developer] = r
		if err := WriteFlows(f); err == nil {
			t.Fatalf("accepted model %q", bad)
		}
	}
}

func TestFlows_DefaultPrompts_CarryEachStepsPlaceholders(t *testing.T) {
	f := DefaultFlows()
	want := map[flow.Role][]string{
		flow.Analyst:   {"{{feature}}", "gh", "plan"},
		flow.Developer: {"{{plan}}", "gh", "pull request"},
		flow.Reviewer:  {"{{pr}}", "gh", "verdict"},
	}
	for role, needles := range want {
		for _, needle := range needles {
			if !strings.Contains(f.Roles[role].Prompt, needle) {
				t.Fatalf("%s prompt lacks %q", role, needle)
			}
		}
	}
	for _, needle := range []string{"{{pr}}", "{{review}}"} {
		if !strings.Contains(f.Roles[flow.Developer].FixPrompt, needle) {
			t.Fatalf("fix prompt lacks %q", needle)
		}
	}
}
