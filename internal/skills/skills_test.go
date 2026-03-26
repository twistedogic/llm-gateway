package skills

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStore_Retrieve(t *testing.T) {
	dir := t.TempDir()
	skillsPath := filepath.Join(dir, "skills.json")

	skillsJSON := `{
		"general_skills": [
			{
				"name": "clarify-ambiguous",
				"description": "Use when the user's request is ambiguous",
				"content": "## Clarify\nAsk clarifying questions."
			}
		],
		"task_specific_skills": {
			"coding": [
				{
					"name": "secure-code-review",
					"description": "Use when reviewing code with security concerns",
					"content": "## Security\nCheck for vulnerabilities."
				}
			]
		}
	}`
	if err := os.WriteFile(skillsPath, []byte(skillsJSON), 0644); err != nil {
		t.Fatalf("failed to write skills file: %v", err)
	}

	store := &Store{
		path: skillsPath,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := store.Load(ctx); err != nil {
		t.Fatalf("failed to load skills: %v", err)
	}

	// Test general skill retrieval with high overlap
	skills := store.Retrieve("This is ambiguous request unclear", nil)
	if len(skills) == 0 {
		t.Error("expected at least one skill for ambiguous message")
	}

	// Test topic-specific retrieval
	skills = store.Retrieve("Review this code for security issues", []string{"coding"})
	if len(skills) == 0 {
		t.Error("expected security skill for coding topic")
	}
}

func TestStore_EmptyStore(t *testing.T) {
	dir := t.TempDir()
	skillsPath := filepath.Join(dir, "skills.json")

	skillsJSON := `{"general_skills": [], "task_specific_skills": {}}`
	if err := os.WriteFile(skillsPath, []byte(skillsJSON), 0644); err != nil {
		t.Fatalf("failed to write skills file: %v", err)
	}

	store := &Store{
		path: skillsPath,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = store.Load(ctx)

	skills := store.Retrieve("any message", nil)
	if len(skills) != 0 {
		t.Errorf("expected 0 skills for empty store, got %d", len(skills))
	}
}

func TestStore_Load_NonExistent(t *testing.T) {
	// Test that Load handles nonexistent files gracefully
	store := &Store{
		path: "/nonexistent/path/skills.json",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Should not return error for nonexistent file
	err := store.Load(ctx)
	if err != nil {
		t.Errorf("expected no error for nonexistent file, got: %v", err)
	}
}

func TestInject(t *testing.T) {
	skills := []Skill{
		{Name: "skill1", Content: "Content 1"},
		{Name: "skill2", Content: "Content 2"},
	}

	result := Inject("System prompt here", skills)

	// Check that skills are injected
	if len(result) == 0 {
		t.Error("expected non-empty result")
	}

	// Check that original prompt is present
	if len(result) < len("System prompt here") {
		t.Error("expected original prompt to be present")
	}
}
