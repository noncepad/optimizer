package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// TestListResearchPapersImpl confirms only .pdf/.txt/.md files directly
// in the directory are listed -- other extensions and subdirectories are
// silently skipped, matching go-wiki/client/hedgefund's own
// loadLocalDocuments convention (flat drop folder, not recursive).
func TestListResearchPapersImpl(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "a.pdf", "not a real pdf, just bytes for the listing")
	writeTestFile(t, dir, "b.txt", "hello")
	writeTestFile(t, dir, "c.md", "# notes")
	writeTestFile(t, dir, "d.jpg", "ignored")
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}

	out, err := listResearchPapersImpl(dir)
	if err != nil {
		t.Fatalf("listResearchPapersImpl: %v", err)
	}
	for _, want := range []string{"a.pdf", "b.txt", "c.md"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in listing, got %q", want, out)
		}
	}
	for _, notWant := range []string{"d.jpg", "subdir"} {
		if strings.Contains(out, notWant) {
			t.Errorf("expected %q NOT in listing, got %q", notWant, out)
		}
	}
}

func TestListResearchPapersImplEmptyDir(t *testing.T) {
	out, err := listResearchPapersImpl(t.TempDir())
	if err != nil {
		t.Fatalf("listResearchPapersImpl: %v", err)
	}
	if !strings.Contains(out, "no .pdf/.txt/.md files found") {
		t.Errorf("expected an explicit empty-directory message, got %q", out)
	}
}

func TestListResearchPapersImplMissingDir(t *testing.T) {
	if _, err := listResearchPapersImpl(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Fatal("expected an error for a missing directory")
	}
}

// TestResolvePaperPath is the important one: filename is an LLM tool
// argument, not a trusted value -- confirms the path-traversal guard
// actually rejects everything it's supposed to, not just the one obvious
// case.
func TestResolvePaperPath(t *testing.T) {
	dir := "/papers"
	cases := []struct {
		name    string
		wantErr bool
	}{
		{"paper.txt", false},
		{"", true},
		{".", true},
		{"..", true},
		{"sub/paper.txt", true},
		{"../secret.txt", true},
		{"../../etc/passwd", true},
		{`win\path.txt`, true},
	}
	for _, c := range cases {
		got, err := resolvePaperPath(dir, c.name)
		if c.wantErr {
			if err == nil {
				t.Errorf("resolvePaperPath(%q): expected an error, got path %q", c.name, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("resolvePaperPath(%q): unexpected error: %v", c.name, err)
			continue
		}
		want := filepath.Join(dir, c.name)
		if got != want {
			t.Errorf("resolvePaperPath(%q) = %q, want %q", c.name, got, want)
		}
	}
}

func TestReadResearchPaperImplText(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	const content = "momentum reverts after 3 days in thin liquidity pairs"
	writeTestFile(t, dir, "note.txt", content)

	got, err := readResearchPaperImpl(ctx, dir, "note.txt")
	if err != nil {
		t.Fatalf("readResearchPaperImpl: %v", err)
	}
	if !strings.Contains(got, content) {
		t.Errorf("expected content %q, got %q", content, got)
	}
}

func TestReadResearchPaperImplUnsupportedExt(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeTestFile(t, dir, "image.jpg", "not readable")

	if _, err := readResearchPaperImpl(ctx, dir, "image.jpg"); err == nil {
		t.Fatal("expected an error for an unsupported extension")
	}
}

// TestReadResearchPaperImplPathTraversal confirms a traversal attempt is
// rejected before ever opening a file, using a real secret file placed
// just outside the configured directory to prove it's genuinely
// unreachable, not just that the string check fires in isolation.
func TestReadResearchPaperImplPathTraversal(t *testing.T) {
	ctx := context.Background()
	parent := t.TempDir()
	papersDir := filepath.Join(parent, "papers")
	if err := os.Mkdir(papersDir, 0o755); err != nil {
		t.Fatalf("mkdir papers: %v", err)
	}
	writeTestFile(t, parent, "secret.txt", "should never be readable via the research agent")

	if _, err := readResearchPaperImpl(ctx, papersDir, "../secret.txt"); err == nil {
		t.Fatal("expected an error for a path-traversal filename")
	}
}

func TestReadResearchPaperImplMissingFile(t *testing.T) {
	ctx := context.Background()
	if _, err := readResearchPaperImpl(ctx, t.TempDir(), "missing.txt"); err == nil {
		t.Fatal("expected an error for a nonexistent file")
	}
}

// TestReadResearchPaperImplTruncation confirms a document longer than
// maxPaperChars is truncated with an explicit notice rather than either
// silently cut with no indication or returned in full (which could blow
// a model's context window).
func TestReadResearchPaperImplTruncation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeTestFile(t, dir, "long.txt", strings.Repeat("x", maxPaperChars+5000))

	got, err := readResearchPaperImpl(ctx, dir, "long.txt")
	if err != nil {
		t.Fatalf("readResearchPaperImpl: %v", err)
	}
	if !strings.Contains(got, "truncated") {
		t.Errorf("expected an explicit truncation notice, got a %d-char response with no notice", len(got))
	}
	if len(got) >= maxPaperChars+5000 {
		t.Errorf("expected the response to actually be shorter than the source document, got %d chars", len(got))
	}
}

func TestResearchToolSet(t *testing.T) {
	ctx := context.Background()
	tools, err := researchTools(t.TempDir())
	if err != nil {
		t.Fatalf("researchTools: %v", err)
	}
	want := map[string]bool{"list_research_papers": false, "read_research_paper": false}
	if len(tools) != len(want) {
		t.Fatalf("expected %d tools, got %d", len(want), len(tools))
	}
	for _, tl := range tools {
		info, err := tl.Info(ctx)
		if err != nil {
			t.Fatalf("Info: %v", err)
		}
		if _, ok := want[info.Name]; !ok {
			t.Errorf("unexpected tool %q", info.Name)
			continue
		}
		want[info.Name] = true
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("expected tool %q to be present", name)
		}
	}
}

// TestNewResearchAgentBuildsWithoutFilesystemAccess confirms agent
// construction succeeds even against a directory that doesn't exist --
// building the tool wrappers never reads the directory, only invoking
// list_research_papers/read_research_paper does.
func TestNewResearchAgentBuildsWithoutFilesystemAccess(t *testing.T) {
	ctx := context.Background()
	agent, err := NewResearchAgent(ctx, &fakeToolCallingModel{response: "ok"}, "/does/not/exist")
	if err != nil {
		t.Fatalf("NewResearchAgent: %v", err)
	}
	if agent == nil {
		t.Fatal("expected a non-nil agent")
	}
}

func TestRunResearchRoundTrip(t *testing.T) {
	ctx := context.Background()
	cm := &fakeToolCallingModel{response: "no actionable ideas found"}
	agent, err := NewResearchAgent(ctx, cm, t.TempDir())
	if err != nil {
		t.Fatalf("NewResearchAgent: %v", err)
	}
	findings, err := RunResearch(ctx, agent, "anything new?")
	if err != nil {
		t.Fatalf("RunResearch: %v", err)
	}
	if findings.Summary != "no actionable ideas found" {
		t.Errorf("unexpected summary: %q", findings.Summary)
	}
}

func TestRunResearchError(t *testing.T) {
	ctx := context.Background()
	cm := &fakeToolCallingModel{err: errors.New("simulated model failure")}
	agent, err := NewResearchAgent(ctx, cm, t.TempDir())
	if err != nil {
		t.Fatalf("NewResearchAgent: %v", err)
	}
	if _, err := RunResearch(ctx, agent, "anything"); err == nil {
		t.Fatal("expected an error from RunResearch")
	}
}
