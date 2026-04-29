package orchestrator

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/moltingduck/duckllo/internal/runner/client"
)

func TestLatestWorkspaceDiff_PicksMostRecent(t *testing.T) {
	b := &client.Bundle{
		Verifications: []client.Verification{
			{ID: uuid.New(), Kind: "lint", Status: "pass"},
			{ID: uuid.New(), Kind: "workspace_changes", Status: "pass",
				Details: []byte(`{"diff":"--- old\n+++ new\n+first"}`)},
			{ID: uuid.New(), Kind: "judge", Status: "pass"},
			{ID: uuid.New(), Kind: "workspace_changes", Status: "pass",
				Details: []byte(`{"diff":"--- old\n+++ new\n+latest"}`)},
		},
	}
	got := latestWorkspaceDiff(b)
	if !strings.Contains(got, "+latest") {
		t.Errorf("expected the latest diff, got %q", got)
	}
}

func TestLatestWorkspaceDiff_ReturnsEmptyOnCleanTree(t *testing.T) {
	b := &client.Bundle{
		Verifications: []client.Verification{
			{ID: uuid.New(), Kind: "workspace_changes", Status: "warn",
				Details: []byte(`{"diff":""}`)},
		},
	}
	if got := latestWorkspaceDiff(b); got != "" {
		t.Errorf("clean tree should yield empty diff; got %q", got)
	}
}

func TestLatestWorkspaceDiff_ReturnsEmptyWhenAbsent(t *testing.T) {
	b := &client.Bundle{
		Verifications: []client.Verification{
			{ID: uuid.New(), Kind: "lint", Status: "pass"},
			{ID: uuid.New(), Kind: "judge", Status: "pass"},
		},
	}
	if got := latestWorkspaceDiff(b); got != "" {
		t.Errorf("got %q want empty", got)
	}
}

func TestUserPromptFor_ValidatorIncludesDiff(t *testing.T) {
	b := &client.Bundle{
		Verifications: []client.Verification{
			{Kind: "workspace_changes",
				Details: []byte(`{"diff":"--- a/x\n+++ b/x\n+hello"}`)},
		},
	}
	b.Spec.Title = "test"
	b.Spec.AcceptanceCriteria = []byte("[]")

	out := userPromptFor("validator", b)
	if !strings.Contains(out, "## Workspace changes") {
		t.Errorf("validator prompt missing workspace section:\n%s", out)
	}
	if !strings.Contains(out, "+hello") {
		t.Errorf("validator prompt missing diff content:\n%s", out)
	}
	if !strings.Contains(out, "```diff") {
		t.Errorf("validator prompt missing fenced diff block:\n%s", out)
	}
}

func TestUserPromptFor_PlannerOmitsDiff(t *testing.T) {
	// The planner has no workspace state yet (it's drafting the plan
	// before any execute phase), so it should never see a diff section.
	b := &client.Bundle{
		Verifications: []client.Verification{
			{Kind: "workspace_changes",
				Details: []byte(`{"diff":"+something"}`)},
		},
	}
	b.Spec.Title = "test"
	b.Spec.AcceptanceCriteria = []byte("[]")

	out := userPromptFor("planner", b)
	if strings.Contains(out, "Workspace changes") {
		t.Errorf("planner prompt should not include diff section:\n%s", out)
	}
}
