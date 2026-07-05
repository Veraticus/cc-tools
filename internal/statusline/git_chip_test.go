package statusline

import (
	"strings"
	"testing"
)

func TestGitChip_BranchOnly(t *testing.T) {
	s := newTestStatusline(t, newTestResolver(t, ""))
	data := &CachedData{GitBranch: "main"}

	comp := s.createGitComponent(data, 25)

	if comp.Color != "sky" {
		t.Errorf("chip bg should be sky, got %q", comp.Color)
	}
	if !strings.Contains(comp.Text, "main") {
		t.Errorf("expected branch name in body, got %q", comp.Text)
	}
	if strings.Contains(comp.Text, "!") {
		t.Errorf("clean branch-only chip should not show a dirty count, got %q", comp.Text)
	}
}

func TestGitChip_DirtyAndAhead(t *testing.T) {
	s := newTestStatusline(t, newTestResolver(t, ""))
	data := &CachedData{GitBranch: "main", GitDirtyCount: 3, GitAhead: 2}

	comp := s.createGitComponent(data, 25)

	if !strings.Contains(comp.Text, "main !3 ↑2") {
		t.Errorf("expected body to contain %q, got %q", "main !3 ↑2", comp.Text)
	}
	if strings.Contains(comp.Text, "↓") {
		t.Errorf("behind marker should be absent when Behind=0, got %q", comp.Text)
	}
}

func TestGitChip_BehindOnly(t *testing.T) {
	s := newTestStatusline(t, newTestResolver(t, ""))
	data := &CachedData{GitBranch: "main", GitBehind: 1}

	comp := s.createGitComponent(data, 25)

	if !strings.Contains(comp.Text, "main ↓1") {
		t.Errorf("expected body to contain %q, got %q", "main ↓1", comp.Text)
	}
	if strings.Contains(comp.Text, "!") || strings.Contains(comp.Text, "↑") {
		t.Errorf("dirty/ahead markers should be absent, got %q", comp.Text)
	}
}

func TestGitChip_PRGlyph_Approved(t *testing.T) {
	s := newTestStatusline(t, newTestResolver(t, ""))
	data := &CachedData{GitBranch: "main", PR: &PRInput{ReviewState: "approved"}}

	comp := s.createGitComponent(data, 25)

	if !strings.Contains(comp.Text, PRIcon) {
		t.Fatalf("expected PR glyph in body, got %q", comp.Text)
	}
	if !strings.Contains(comp.Text, CatppuccinMocha{}.GreenFG()+PRIcon) {
		t.Errorf("approved review_state should color the glyph green, got %q", comp.Text)
	}
}

func TestGitChip_PRGlyph_PendingOrEmptyIsYellow(t *testing.T) {
	s := newTestStatusline(t, newTestResolver(t, ""))

	for _, reviewState := range []string{"pending", ""} {
		data := &CachedData{GitBranch: "main", PR: &PRInput{ReviewState: reviewState}}
		comp := s.createGitComponent(data, 25)

		if !strings.Contains(comp.Text, CatppuccinMocha{}.YellowFG()+PRIcon) {
			t.Errorf("review_state %q should color the glyph yellow, got %q", reviewState, comp.Text)
		}
	}
}

func TestGitChip_PRGlyph_ChangesRequestedIsRed(t *testing.T) {
	s := newTestStatusline(t, newTestResolver(t, ""))
	data := &CachedData{GitBranch: "main", PR: &PRInput{ReviewState: "changes_requested"}}

	comp := s.createGitComponent(data, 25)

	if !strings.Contains(comp.Text, CatppuccinMocha{}.RedFG()+PRIcon) {
		t.Errorf("changes_requested should color the glyph red, got %q", comp.Text)
	}
}

func TestGitChip_PRGlyph_DraftIsMutedOverlay(t *testing.T) {
	s := newTestStatusline(t, newTestResolver(t, ""))
	data := &CachedData{GitBranch: "main", PR: &PRInput{ReviewState: "draft"}}

	comp := s.createGitComponent(data, 25)

	if !strings.Contains(comp.Text, CatppuccinMocha{}.OverlayFG()+PRIcon) {
		t.Errorf("draft should color the glyph with the muted overlay gray, got %q", comp.Text)
	}
}

func TestGitChip_NoPRGlyphWhenPRNil(t *testing.T) {
	s := newTestStatusline(t, newTestResolver(t, ""))
	data := &CachedData{GitBranch: "main"}

	comp := s.createGitComponent(data, 25)

	if strings.Contains(comp.Text, PRIcon) {
		t.Errorf("no PR glyph expected when data.PR is nil, got %q", comp.Text)
	}
}
