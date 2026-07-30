package git

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"got/utils"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

const (
	email     = "test@example.com"
	name      = "Test User"
	timestamp = int64(1785415052)
	timezone  = 18000
)

// setupGitRepo creates a temp directory, initializes a real git repo,
// configures user, and returns cleanup function.
func setupGitRepo(t *testing.T) (repoDir string, cleanup func()) {
	t.Helper()

	dir, err := os.MkdirTemp("", "got-test-*")
	if err != nil {
		t.Fatal(err)
	}

	origDir, err := os.Getwd()
	if err != nil {
		os.RemoveAll(dir)
		t.Fatal(err)
	}

	if err := os.Chdir(dir); err != nil {
		os.RemoveAll(dir)
		t.Fatal(err)
	}

	// Configure default branch
	exec.Command("git", "config", "--global", "init.defaultBranch", "main")

	// Initialize git repo using real git
	cmd := exec.Command("git", "init")
	if out, err := cmd.CombinedOutput(); err != nil {
		os.Chdir(origDir)
		os.RemoveAll(dir)
		t.Fatalf("git init failed: %v, output: %s", err, out)
	}

	// Configure git user for commits
	exec.Command("git", "config", "user.email", email).Run()
	exec.Command("git", "config", "user.name", name).Run()

	// Set date for commits
	ts := strconv.FormatInt(timestamp, 10)
	tz := utils.FormatTimezone(timezone)
	err = os.Setenv("GIT_COMMITTER_DATE", tz+" "+ts)
	if err != nil {
		os.Chdir(origDir)
		os.RemoveAll(dir)
		t.Fatalf("GIT_COMMITTER_DATE editing error: %v", err)
	}
	err = os.Setenv("GIT_AUTHOR_DATE", tz+" "+ts)
	if err != nil {
		os.Chdir(origDir)
		os.RemoveAll(dir)
		t.Fatalf("GIT_AUTHOR_DATE editing error: %v", err)
	}

	cleanup = func() {
		os.Chdir(origDir)
		os.RemoveAll(dir)
		os.Unsetenv("GIT_COMMITTER_DATE")
		os.Unsetenv("GIT_AUTHOR_DATE")
	}

	return dir, cleanup
}

// runGit executes a git command and returns trimmed stdout.
func runGit(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v, output: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// createFile writes content to a file.
func createFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// ============================================================================
// TestInit
// ============================================================================

func TestInit(t *testing.T) {
	dir := t.TempDir()
	origDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(origDir)

	Init()

	// Check that required directories exist
	for _, d := range []string{".git", ".git/objects", ".git/refs"} {
		if info, err := os.Stat(d); os.IsNotExist(err) {
			t.Errorf("Init: expected directory %s to exist", d)
		} else if !info.IsDir() {
			t.Errorf("Init: %s is not a directory", d)
		}
	}

	// Check HEAD file content
	head, err := os.ReadFile(".git/HEAD")
	if err != nil {
		t.Fatal(err)
	}
	expected := "ref: refs/heads/main\n"
	if string(head) != expected {
		t.Errorf("Init: HEAD content mismatch:\n  expected: %q\n  actual:   %q", expected, string(head))
	}
}

// ============================================================================
// TestHashObject
// ============================================================================

func TestHashObject(t *testing.T) {
	_, cleanup := setupGitRepo(t)
	defer cleanup()

	// Create a test file
	content := "hello world\n"
	createFile(t, "test.txt", content)

	// Get SHA from real git
	expectedSHA := runGit(t, "hash-object", "-w", "test.txt")

	// Get SHA from got
	actualSHA, err := HashObject([]byte(content))
	if err != nil {
		t.Fatal(err)
	}

	if expectedSHA != actualSHA {
		t.Errorf("HashObject SHA mismatch:\n  expected: %s\n  actual:   %s", expectedSHA, actualSHA)
	}
}

func TestHashObject_EmptyFile(t *testing.T) {
	_, cleanup := setupGitRepo(t)
	defer cleanup()

	createFile(t, "empty.txt", "")

	expectedSHA := runGit(t, "hash-object", "-w", "empty.txt")
	actualSHA, err := HashObject([]byte(""))
	if err != nil {
		t.Fatal(err)
	}

	if expectedSHA != actualSHA {
		t.Errorf("HashObject (empty file) SHA mismatch:\n  expected: %s\n  actual:   %s", expectedSHA, actualSHA)
	}
}

func TestHashObject_BinaryFile(t *testing.T) {
	_, cleanup := setupGitRepo(t)
	defer cleanup()

	binaryContent := []byte{0x00, 0x01, 0x02, 0xFF, 0xFE}
	if err := os.WriteFile("binary.bin", binaryContent, 0644); err != nil {
		t.Fatal(err)
	}

	expectedSHA := runGit(t, "hash-object", "-w", "binary.bin")
	content, err := os.ReadFile("binary.bin")
	if err != nil {
		t.Errorf("can`t read a file: %s", err.Error())
	}
	actualSHA, err := HashObject(content)
	if err != nil {
		t.Fatal(err)
	}

	if expectedSHA != actualSHA {
		t.Errorf("HashObject (binary) SHA mismatch:\n  expected: %s\n  actual:   %s", expectedSHA, actualSHA)
	}
}

func TestHashObject_ExistingDifferentObjectCollision(t *testing.T) {
	_, cleanup := setupGitRepo(t)
	defer cleanup()

	content := "hello world\n"
	createFile(t, "test.txt", content)

	file, err := os.ReadFile("test.txt")
	if err != nil {
		t.Errorf("can`t read a file: %s", err.Error())
	}
	sha, err := HashObject(file)
	if err != nil {
		t.Fatal(err)
	}

	path := fmt.Sprintf(".git/objects/%s/%s", sha[:2], sha[2:])
	fakeObject := []byte("blob 8\x00badstuff")
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	if _, err := zw.Write(fakeObject); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, buf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}

	file, err = os.ReadFile("test.txt")
	if err != nil {
		t.Errorf("can`t read a file: %s", err.Error())
	}
	_, err = HashObject(file)
	if err == nil {
		t.Fatal("expected hash collision error, got nil")
	}
	if !strings.Contains(err.Error(), "hash collision") {
		t.Fatalf("expected hash collision error, got %v", err)
	}
}

// ============================================================================
// TestCatFile
// ============================================================================

func TestCatFile(t *testing.T) {
	_, cleanup := setupGitRepo(t)
	defer cleanup()

	content := "hello world\n"
	createFile(t, "test.txt", content)

	sha := runGit(t, "hash-object", "-w", "test.txt")

	// Get content from real git (pretty-print)
	expectedContent := runGit(t, "cat-file", "-p", sha)

	// Get content from got
	gotRaw := CatFile(sha)

	if expectedContent != gotRaw {
		t.Errorf("CatFile content mismatch:\n  expected: %q\n  actual:   %q", expectedContent, gotRaw)
	}
}

// ============================================================================
// TestLsTree
// ============================================================================

func TestLsTree(t *testing.T) {
	_, cleanup := setupGitRepo(t)
	defer cleanup()

	// Create files and stage them
	createFile(t, "file1.txt", "content1\n")
	createFile(t, "file2.txt", "content2\n")

	runGit(t, "add", "file1.txt", "file2.txt")

	// Get tree SHA from real git
	treeSHA := runGit(t, "write-tree")

	// Get tree listing from real git
	expectedOutput := runGit(t, "ls-tree", treeSHA)

	// Get tree listing from got
	nodes, err := LsTree(treeSHA)
	if err != nil {
		t.Fatal(err)
	}

	// Build string from nodes for comparison
	var gotBuf bytes.Buffer
	for _, node := range nodes {
		modeStr := strings.TrimLeft(node.Mode.String(), "0")
		gotBuf.WriteString(fmt.Sprintf("%s blob %s\t%s\n", modeStr, node.Sha1, node.Name))
	}
	gotOutput := strings.TrimSpace(gotBuf.String())

	if expectedOutput != gotOutput {
		t.Errorf("LsTree mismatch:\n  expected:\n%s\n  actual:\n%s", expectedOutput, gotOutput)
	}
}

func TestLsTree_WithSubdirectory(t *testing.T) {
	_, cleanup := setupGitRepo(t)
	defer cleanup()

	// Create files including in subdirectory
	createFile(t, "root.txt", "root\n")
	os.MkdirAll("subdir", 0755)
	createFile(t, "subdir/nested.txt", "nested\n")

	runGit(t, "add", ".")

	// Get tree SHA from real git
	treeSHA := runGit(t, "write-tree")

	// Get tree listing from real git
	expectedOutput := runGit(t, "ls-tree", treeSHA) + "\n"

	// Get tree listing from got
	nodes, err := LsTree(treeSHA)
	if err != nil {
		t.Fatal(err)
	}

	var gotBuf bytes.Buffer
	for _, node := range nodes {
		//modeStr := node.Mode.String()
		objType := "blob"
		if node.Mode == Dir {
			objType = "tree"
		}
		stroke, _ := strings.CutPrefix(fmt.Sprintf("%s %s %s\t%s\n", node.Mode, objType, node.Sha1, node.Name), "0")
		gotBuf.WriteString(stroke)
	}
	gotOutput := gotBuf.String()

	if expectedOutput != gotOutput {
		t.Errorf("LsTree (with subdir) mismatch:\n  expected:\n%s\n  actual:\n%s", expectedOutput, gotOutput)
	}
}

// ============================================================================
// TestWriteTree
// ============================================================================

func TestWriteTree(t *testing.T) {
	_, cleanup := setupGitRepo(t)
	defer cleanup()

	createFile(t, "a.txt", "aaa\n")
	createFile(t, "b.txt", "bbb\n")

	runGit(t, "add", ".")

	// Get tree SHA from real git
	expectedSHA := runGit(t, "write-tree")

	// Get tree SHA from got
	actualSHA, err := WriteTree(".")
	if err != nil {
		t.Fatal(err)
	}

	if expectedSHA != actualSHA {
		t.Errorf("WriteTree SHA mismatch:\n  expected: %s\n  actual:   %s", expectedSHA, actualSHA)
	}
}

func TestWriteTree_WithSubdirectory(t *testing.T) {
	_, cleanup := setupGitRepo(t)
	defer cleanup()

	createFile(t, "root.txt", "root\n")
	os.MkdirAll("sub", 0755)
	createFile(t, "sub/nested.txt", "nested\n")

	runGit(t, "add", ".")

	expectedSHA := runGit(t, "write-tree")
	actualSHA, err := WriteTree(".")
	if err != nil {
		t.Fatal(err)
	}

	if expectedSHA != actualSHA {
		t.Errorf("WriteTree (with subdir) SHA mismatch:\n  expected: %s\n  actual:   %s", expectedSHA, actualSHA)
	}
}

// ============================================================================
// TestCommitTree
// ============================================================================

func TestCommitTree(t *testing.T) {
	_, cleanup := setupGitRepo(t)
	defer cleanup()

	createFile(t, "file.txt", "content\n")
	runGit(t, "add", ".")
	treeSHA := runGit(t, "write-tree")

	// Create commit using real git
	expectedSHA := runGit(t, "commit-tree", treeSHA, "-m", "initial commit")

	// Create commit using got
	commit := Commit{
		TreeSHA: treeSHA,
		Message: "initial commit",
		Author: Author{
			Name:      "Test User",
			Email:     "test@example.com",
			Timestamp: timestamp,
			Timezone:  timezone,
		},
		Committer: Committer{
			Name:      "Test User",
			Email:     "test@example.com",
			Timestamp: timestamp,
			Timezone:  timezone,
		},
	}

	actualSHA, err := CommitTree(commit)
	if err != nil {
		t.Fatal(err)
	}

	if expectedSHA != actualSHA {
		t.Errorf("CommitTree SHA mismatch:\n  expected: %s\n  actual:   %s", expectedSHA, actualSHA)
	}
}

func TestCommitTree_WithParent(t *testing.T) {
	_, cleanup := setupGitRepo(t)
	defer cleanup()

	// First commit
	createFile(t, "f1.txt", "first\n")
	runGit(t, "add", ".")
	treeSHA1 := runGit(t, "write-tree")
	parentSHA := runGit(t, "commit-tree", treeSHA1, "-m", "first commit")

	// Second commit
	createFile(t, "f2.txt", "second\n")
	runGit(t, "add", ".")
	treeSHA2 := runGit(t, "write-tree")

	expectedSHA := runGit(t, "commit-tree", treeSHA2, "-p", parentSHA, "-m", "second commit")

	commit := Commit{
		TreeSHA:   treeSHA2,
		ParentSHA: parentSHA,
		Message:   "second commit",
		Author: Author{
			Name:      "Test User",
			Email:     "test@example.com",
			Timestamp: timestamp,
			Timezone:  timezone,
		},
		Committer: Committer{
			Name:      "Test User",
			Email:     "test@example.com",
			Timestamp: timestamp,
			Timezone:  timezone,
		},
	}

	actualSHA, err := CommitTree(commit)
	if err != nil {
		t.Fatal(err)
	}

	if expectedSHA != actualSHA {
		t.Errorf("CommitTree (with parent) SHA mismatch:\n  expected: %s\n  actual:   %s", expectedSHA, actualSHA)
	}
}

// ============================================================================
// TestReadHead / TestUpdateHead
// ============================================================================

func TestReadHead(t *testing.T) {
	_, cleanup := setupGitRepo(t)
	defer cleanup()

	// After git init, HEAD should point to main
	head, err := ReadHead()
	if err != nil {
		t.Fatal(err)
	}

	expected := "refs/heads/main\n"
	if head.Ref != expected {
		t.Errorf("ReadHead mismatch:\n  expected: %q\n  actual:   %q", expected, head.Ref)
	}
}

func TestUpdateHead(t *testing.T) {
	_, cleanup := setupGitRepo(t)
	defer cleanup()

	// Update HEAD using got
	newRef := "refs/heads/develop"
	if err := UpdateHead(newRef); err != nil {
		t.Fatal(err)
	}

	// Read HEAD using real git
	actualRef := runGit(t, "symbolic-ref", "HEAD")

	if newRef != actualRef {
		t.Errorf("UpdateHead mismatch:\n  expected: %s\n  actual:   %s", newRef, actualRef)
	}
}

// ============================================================================
// TestReadReference / TestUpdateReference
// ============================================================================

func TestUpdateAndReadReference(t *testing.T) {
	_, cleanup := setupGitRepo(t)
	defer cleanup()

	// Create a commit to have a real SHA
	createFile(t, "f.txt", "data\n")
	runGit(t, "add", ".")
	treeSHA := runGit(t, "write-tree")
	commitSHA := runGit(t, "commit-tree", treeSHA, "-m", "test")

	// Update reference using got
	ref := Reference{
		Name: "heads/my-branch",
		Sha1: commitSHA,
	}
	if err := UpdateReference(ref); err != nil {
		t.Fatal(err)
	}

	// Read reference using got
	readRef, err := ReadReference("heads/my-branch")
	if err != nil {
		t.Fatal(err)
	}

	if readRef.Name != ref.Name {
		t.Errorf("ReadReference name mismatch:\n  expected: %s\n  actual:   %s", ref.Name, readRef.Name)
	}
	if readRef.Sha1 != ref.Sha1 {
		t.Errorf("ReadReference SHA mismatch:\n  expected: %s\n  actual:   %s", ref.Sha1, readRef.Sha1)
	}

	// Verify with real git
	gitSHA := runGit(t, "rev-parse", "my-branch")
	if gitSHA != commitSHA {
		t.Errorf("Reference not found by real git:\n  expected: %s\n  actual:   %s", commitSHA, gitSHA)
	}
}

// ============================================================================
// TestCreateTag / TestReadTag
// ============================================================================

func TestCreateAndReadTag(t *testing.T) {
	_, cleanup := setupGitRepo(t)
	defer cleanup()

	// Create a commit
	createFile(t, "f.txt", "data\n")
	runGit(t, "add", ".")
	treeSHA := runGit(t, "write-tree")
	commitSHA := runGit(t, "commit-tree", treeSHA, "-m", "test")

	tagName := "v1.0"
	// Create tag using got
	if err := CreateTag(tagName, commitSHA); err != nil {
		t.Fatal(err)
	}

	// Read tag using got
	tag, err := ReadTag(tagName)
	if err != nil {
		t.Fatal(err)
	}

	if tag.Name != tagName {
		t.Errorf("ReadTag name mismatch:\n  expected: tags/v1.0\n  actual:   %s", tag.Name)
	}
	if tag.Sha1 != commitSHA {
		t.Errorf("ReadTag SHA mismatch:\n  expected: %s\n  actual:   %s", commitSHA, tag.Sha1)
	}

	// Verify with real git
	gitSHA := runGit(t, "rev-parse", tagName)
	if gitSHA != commitSHA {
		t.Errorf("Tag not found by real git:\n  expected: %s\n  actual:   %s", commitSHA, gitSHA)
	}
}

// ============================================================================
// TestReadAnnotatedTag
// ============================================================================

func TestReadAnnotatedTag(t *testing.T) {
	d, cleanup := setupGitRepo(t)
	fmt.Println(d)
	defer cleanup()

	// Create a commit
	createFile(t, "f.txt", "data\n")
	runGit(t, "add", ".")
	treeSHA := runGit(t, "write-tree")
	commitSHA := runGit(t, "commit-tree", treeSHA, "-m", "test")

	// Create annotated tag using real git
	runGit(t, "tag", "-a", "v1.0-annotated", "-m", "annotated tag message", commitSHA)

	// Read annotated tag using got
	tag, err := ReadAnnotatedTag("v1.0-annotated")
	if err != nil {
		t.Fatal(err)
	}

	if tag.Name != "v1.0-annotated" {
		t.Errorf("ReadAnnotatedTag name mismatch:\n  expected: v1.0-annotated\n  actual:   %s", tag.Name)
	}
	if tag.TaggedObjectSha1 != commitSHA {
		t.Errorf("ReadAnnotatedTag object SHA mismatch:\n  expected: %s\n  actual:   %s", commitSHA, tag.TaggedObjectSha1)
	}
	if tag.TaggedObjectType != "commit" {
		t.Errorf("ReadAnnotatedTag object type mismatch:\n  expected: commit\n  actual:   %s", tag.TaggedObjectType)
	}
	if tag.Tagger.Email != email {
		t.Errorf("ReadAnnotatedTag tagger email mismatch:\n  expected: %s\n  actual:   %s", email, tag.Tagger.Email)
	}
	if tag.Tagger.Name != name {
		t.Errorf("ReadAnnotatedTag tagger name mismatch:\n  expected: %s\n  actual:   %s", name, tag.Tagger.Name)
	}
	if tag.Tagger.Timezone != timezone {
		t.Errorf("ReadAnnotatedTag tagger timezone mismatch:\n  expected: %d\n  actual:   %d", timezone, tag.Tagger.Timezone)
	}
	if tag.Message != "annotated tag message" {
		t.Errorf("ReadAnnotatedTag message mismatch:\n  expected: annotated tag message\n  actual:   %s", tag.Message)
	}
}

// ============================================================================
// TestFileMode
// ============================================================================

func TestFileMode_New(t *testing.T) {
	tests := []struct {
		input    string
		expected FileMode
	}{
		{"100644", Regular},
		{"100755", Executable},
		{"120000", SymLink},
		{"040000", Dir},
		{"160000", Submodule},
		{"0", Empty},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			fm, err := New(tt.input)
			if err != nil {
				t.Fatal(err)
			}
			if fm != tt.expected {
				t.Errorf("New(%q) = %d, want %d", tt.input, fm, tt.expected)
			}
		})
	}
}

func TestFileMode_String(t *testing.T) {
	tests := []struct {
		mode     FileMode
		expected string
	}{
		{Regular, "0100644"},
		{Executable, "0100755"},
		{SymLink, "0120000"},
		{Dir, "0040000"},
		{Submodule, "0160000"},
		{Empty, "0000000"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if got := tt.mode.String(); got != tt.expected {
				t.Errorf("FileMode.String() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestGetMode(t *testing.T) {
	dir := t.TempDir()
	origDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(origDir)

	// Regular file
	createFile(t, "regular.txt", "hello")
	mode, err := GetMode("regular.txt")
	if err != nil {
		t.Fatal(err)
	}
	if mode != Regular {
		t.Errorf("GetMode(regular.txt) = %d, want %d", mode, Regular)
	}

	// Executable file
	createFile(t, "exec.sh", "#!/bin/sh\n")
	os.Chmod("exec.sh", 0755)
	mode, err = GetMode("exec.sh")
	if err != nil {
		t.Fatal(err)
	}
	if mode != Executable {
		t.Errorf("GetMode(exec.sh) = %d, want %d", mode, Executable)
	}

	// Directory
	os.MkdirAll("mydir", 0755)
	mode, err = GetMode("mydir")
	if err != nil {
		t.Fatal(err)
	}
	if mode != Dir {
		t.Errorf("GetMode(mydir) = %d, want %d", mode, Dir)
	}
}

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

// ============================================================================
// TestListLocalBranches
// ============================================================================

func TestListLocalBranches(t *testing.T) {
	_, cleanup := setupGitRepo(t)
	defer cleanup()

	// Create a commit on main
	createFile(t, "f.txt", "data\n")
	runGit(t, "add", ".")
	treeSHA := runGit(t, "write-tree")
	commitSHA := runGit(t, "commit-tree", treeSHA, "-m", "test")
	runGit(t, "update-ref", "refs/heads/main", commitSHA)

	// Create another branch
	runGit(t, "branch", "feature-branch", commitSHA)

	// List branches using got
	branches, err := ListLocalBranches()
	if err != nil {
		t.Fatal(err)
	}

	// Check that both branches are found
	found := make(map[string]bool)
	for _, b := range branches {
		found[b.Name] = true
	}

	if !found["heads/main"] {
		t.Error("ListLocalBranches: heads/main not found")
	}
	if !found["heads/feature-branch"] {
		t.Error("ListLocalBranches: heads/feature-branch not found")
	}
}

// ============================================================================
// TestCreateTree (integration with real git)
// ============================================================================

func TestCreateTree_Integration(t *testing.T) {
	_, cleanup := setupGitRepo(t)
	defer cleanup()

	// Create files
	createFile(t, "a.txt", "aaa\n")
	createFile(t, "b.txt", "bbb\n")
	os.MkdirAll("sub", 0755)
	createFile(t, "sub/c.txt", "ccc\n")

	// Stage and write tree with real git
	runGit(t, "add", ".")
	expectedTreeSHA := runGit(t, "write-tree")

	// Write tree with got
	actualTreeSHA, err := WriteTree(".")
	if err != nil {
		t.Fatal(err)
	}

	if expectedTreeSHA != actualTreeSHA {
		t.Errorf("WriteTree integration SHA mismatch:\n  expected: %s\n  actual:   %s", expectedTreeSHA, actualTreeSHA)
	}

	// Compare tree contents
	expectedNodes, err := LsTree(expectedTreeSHA)
	if err != nil {
		t.Fatal(err)
	}
	actualNodes, err := LsTree(actualTreeSHA)
	if err != nil {
		t.Fatal(err)
	}

	if len(expectedNodes) != len(actualNodes) {
		t.Fatalf("LsTree node count mismatch: expected %d, got %d", len(expectedNodes), len(actualNodes))
	}

	for i := range expectedNodes {
		if expectedNodes[i].Name != actualNodes[i].Name {
			t.Errorf("Node[%d] name mismatch: expected %s, got %s", i, expectedNodes[i].Name, actualNodes[i].Name)
		}
		if expectedNodes[i].Mode != actualNodes[i].Mode {
			t.Errorf("Node[%s] mode mismatch: expected %d, got %d", expectedNodes[i].Name, expectedNodes[i].Mode, actualNodes[i].Mode)
		}
		if expectedNodes[i].Sha1 != actualNodes[i].Sha1 {
			t.Errorf("Node[%s] SHA mismatch: expected %s, got %s", expectedNodes[i].Name, expectedNodes[i].Sha1, actualNodes[i].Sha1)
		}
	}
}

// ============================================================================
// TestFullWorkflow (end-to-end)
// ============================================================================

func TestFullWorkflow(t *testing.T) {
	_, cleanup := setupGitRepo(t)
	defer cleanup()

	// Step 1: Create files
	createFile(t, "README.md", "# My Project\n")
	createFile(t, "main.go", "package main\n\nfunc main() {}\n")
	os.MkdirAll("lib", 0755)
	createFile(t, "lib/helper.go", "package lib\n\nfunc Help() {}\n")

	// Step 2: Hash objects and compare with real git
	files := []string{"README.md", "main.go", "lib/helper.go"}
	for _, f := range files {
		expectedSHA := runGit(t, "hash-object", "-w", f)

		content, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		actualSHA, err := HashObject(content)
		if err != nil {
			t.Fatal(err)
		}
		if expectedSHA != actualSHA {
			t.Errorf("FullWorkflow: HashObject(%s) mismatch:\n  expected: %s\n  actual:   %s", f, expectedSHA, actualSHA)
		}
	}

	// Step 3: Write tree and compare
	runGit(t, "add", ".")
	expectedTreeSHA := runGit(t, "write-tree")
	actualTreeSHA, err := WriteTree(".")
	if err != nil {
		t.Fatal(err)
	}
	if expectedTreeSHA != actualTreeSHA {
		t.Errorf("FullWorkflow: WriteTree SHA mismatch:\n  expected: %s\n  actual:   %s", expectedTreeSHA, actualTreeSHA)
	}

	// Step 4: Create commit and compare
	expectedCommitSHA := runGit(t, "commit-tree", expectedTreeSHA, "-m", "initial commit")
	commit := Commit{
		TreeSHA: actualTreeSHA,
		Message: "initial commit",
		Author: Author{
			Name:      "Test User",
			Email:     "test@example.com",
			Timestamp: timestamp,
			Timezone:  timezone,
		},
		Committer: Committer{
			Name:      "Test User",
			Email:     "test@example.com",
			Timestamp: timestamp,
			Timezone:  timezone,
		},
	}
	actualCommitSHA, err := CommitTree(commit)
	if err != nil {
		t.Fatal(err)
	}
	if expectedCommitSHA != actualCommitSHA {
		t.Errorf("FullWorkflow: CommitTree SHA mismatch:\n  expected: %s\n  actual:   %s", expectedCommitSHA, actualCommitSHA)
	}

	// Step 5: Update reference and verify
	ref := Reference{Name: "heads/main", Sha1: actualCommitSHA}
	if err := UpdateReference(ref); err != nil {
		t.Fatal(err)
	}

	gitSHA := runGit(t, "rev-parse", "main")
	if gitSHA != actualCommitSHA {
		t.Errorf("FullWorkflow: branch ref mismatch:\n  expected: %s\n  actual:   %s", actualCommitSHA, gitSHA)
	}

	// Step 6: Create tag and verify
	if err := CreateTag("v1.0", actualCommitSHA); err != nil {
		t.Fatal(err)
	}
	gitTagSHA := runGit(t, "rev-parse", "v1.0")
	if gitTagSHA != actualCommitSHA {
		t.Errorf("FullWorkflow: tag ref mismatch:\n  expected: %s\n  actual:   %s", actualCommitSHA, gitTagSHA)
	}
}
