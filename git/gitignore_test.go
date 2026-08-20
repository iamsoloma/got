package git

import (
	"os"
	"testing"
)

// ============================================================================
// TestGitignore
// ============================================================================

func TestReadGitignore(t *testing.T) {
	dir := t.TempDir()
	origDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(origDir)

	// Create .gitignore
	createFile(t, ".gitignore", "*.log\nbuild/\n# comment\n")

	patterns, err := ReadGitignore(".")
	if err != nil {
		t.Fatal(err)
	}

	// Should include /.git and patterns from .gitignore (excluding comments)
	expected := []string{"/.git", "*.log", "build/"}
	if len(patterns) != len(expected) {
		t.Fatalf("ReadGitignore: expected %d patterns, got %d: %v", len(expected), len(patterns), patterns)
	}
	for i, p := range patterns {
		if p != expected[i] {
			t.Errorf("ReadGitignore[%d] = %q, want %q", i, p, expected[i])
		}
	}
}

func TestReadGitignore_NoFile(t *testing.T) {
	dir := t.TempDir()
	origDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(origDir)

	// No .gitignore file
	patterns, err := ReadGitignore(".")
	if err != nil {
		t.Fatal(err)
	}

	// Should only include /.git
	if len(patterns) != 1 || patterns[0] != "/.git" {
		t.Errorf("ReadGitignore (no file) = %v, want [/.git]", patterns)
	}
}

func TestCheckIgnore(t *testing.T) {
	patterns := []string{"/.git", "*.log", "build/"}

	tests := []struct {
		path     string
		expected bool
	}{
		{"/.git", true},
		{"*.log", true},
		{"build/", true},
		{"main.go", false},
		{"readme.md", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := checkIgnore(tt.path, patterns); got != tt.expected {
				t.Errorf("checkIgnore(%q) = %v, want %v", tt.path, got, tt.expected)
			}
		})
	}
}
