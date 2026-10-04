package git

import (
	"got/storage/filesystem"
	"got/utils"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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

// setupGitRepo creates a work tree backed by a bare Git directory.
func setupGitRepo(t *testing.T) (repoDir string, cleanup func()) {
	t.Helper()

	dir, err := os.MkdirTemp("", "got-test-*")
	if err != nil {
		t.Fatal(err)
	}
	repoDir, err = os.MkdirTemp("", "got-bare-*")
	if err != nil {
		os.RemoveAll(dir)
		t.Fatal(err)
	}

	origDir, err := os.Getwd()
	if err != nil {
		os.RemoveAll(dir)
		os.RemoveAll(repoDir)
		t.Fatal(err)
	}

	if err := os.Chdir(dir); err != nil {
		os.RemoveAll(dir)
		os.RemoveAll(repoDir)
		t.Fatal(err)
	}

	// Configure default branch
	exec.Command("git", "config", "--global", "init.defaultBranch", "main").Run()

	// Initialize a bare repository and attach the temporary work tree to it.
	cmd := exec.Command("git", "init", "--bare", repoDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		os.Chdir(origDir)
		os.RemoveAll(dir)
		os.RemoveAll(repoDir)
		t.Fatalf("git init failed: %v, output: %s", err, out)
	}
	t.Setenv("GIT_DIR", repoDir)
	t.Setenv("GIT_WORK_TREE", dir)

	// Configure git user for commits
	for _, args := range [][]string{
		{"config", "user.email", email},
		{"config", "user.name", name},
	} {
		cmd := exec.Command("git", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			os.Chdir(origDir)
			os.RemoveAll(dir)
			os.RemoveAll(repoDir)
			t.Fatalf("git %v failed: %v, output: %s", args, err, out)
		}
	}

	// Set date for commits
	ts := strconv.FormatInt(timestamp, 10)
	tz := utils.FormatTimezone(timezone)
	err = os.Setenv("GIT_COMMITTER_DATE", tz+" "+ts)
	if err != nil {
		os.Chdir(origDir)
		os.RemoveAll(dir)
		os.RemoveAll(repoDir)
		t.Fatalf("GIT_COMMITTER_DATE editing error: %v", err)
	}
	err = os.Setenv("GIT_AUTHOR_DATE", tz+" "+ts)
	if err != nil {
		os.Chdir(origDir)
		os.RemoveAll(dir)
		os.RemoveAll(repoDir)
		t.Fatalf("GIT_AUTHOR_DATE editing error: %v", err)
	}

	cleanup = func() {
		os.Chdir(origDir)
		os.RemoveAll(dir)
		os.RemoveAll(repoDir)
		os.Unsetenv("GIT_COMMITTER_DATE")
		os.Unsetenv("GIT_AUTHOR_DATE")
	}

	return repoDir, cleanup
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

	storage := &filesystem.FileSystemStorage{}
	_, err := Init(storage, "./", "main")
	if err != nil {
		t.Fatal(err)
	}

	// Check that required directories exist
	for _, d := range []string{"objects", "refs"} {
		if info, err := os.Stat(d); os.IsNotExist(err) {
			t.Errorf("Init: expected directory %s to exist", d)
		} else if !info.IsDir() {
			t.Errorf("Init: %s is not a directory", d)
		}
	}

	// Check HEAD file content
	head, err := os.ReadFile("./HEAD")
	if err != nil {
		t.Fatal(err)
	}
	expected := "ref: refs/heads/main\n"
	if string(head) != expected {
		t.Errorf("Init: HEAD content mismatch:\n  expected: %q\n  actual:   %q", expected, string(head))
	}
}

func TestInitCreatesOpenableBareRepository(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bare")
	storage := &filesystem.FileSystemStorage{}
	if _, err := Init(storage, dir, "trunk"); err != nil {
		t.Fatal(err)
	}

	repo, err := Open(&filesystem.FileSystemStorage{}, dir)
	if err != nil {
		t.Fatal(err)
	}
	head, err := repo.ReadHead()
	if err != nil {
		t.Fatal(err)
	}
	if head.Ref != "refs/heads/trunk\n" {
		t.Fatalf("HEAD ref = %q, want %q", head.Ref, "refs/heads/trunk\n")
	}

	out, err := exec.Command("git", "--git-dir", dir, "rev-parse", "--is-bare-repository").CombinedOutput()
	if err != nil {
		t.Fatalf("git did not recognize initialized bare repository: %v, output: %s", err, out)
	}
	if strings.TrimSpace(string(out)) != "true" {
		t.Fatalf("git reports repository is not bare: %q", out)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); !os.IsNotExist(err) {
		t.Fatalf("bare repository unexpectedly contains .git: %v", err)
	}
}

func TestBarePackStorageRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bare")
	repo, err := Init(&filesystem.FileSystemStorage{}, dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	repo, err = Open(&filesystem.FileSystemStorage{}, dir)
	if err != nil {
		t.Fatal(err)
	}

	const packName = "pack-test.pack"
	const content = "bare pack contents"
	writer, err := repo.Storage.PackWriter(packName, int64(len(content)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(writer, content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	exists, err := repo.Storage.PackExists(packName)
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatalf("pack %q was not found in bare repository", packName)
	}
	reader, err := repo.Storage.PackReader(packName)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Fatalf("pack contents = %q, want %q", got, content)
	}

	packs, err := repo.Storage.ListPacks()
	if err != nil {
		t.Fatal(err)
	}
	if len(packs) != 1 || packs[0] != packName {
		t.Fatalf("ListPacks() = %v, want [%s]", packs, packName)
	}
}

// ============================================================================
// TestFullWorkflow (end-to-end)
// ============================================================================

func TestFullWorkflow(t *testing.T) {
	dir, cleanup := setupGitRepo(t)
	defer cleanup()

	// Step 1: Create files
	createFile(t, "README.md", "# My Project\n")
	createFile(t, "main.go", "package main\n\nfunc main() {}\n")
	os.MkdirAll("lib", 0755)
	createFile(t, "lib/helper.go", "package lib\n\nfunc Help() {}\n")

	repo, err := Open(&filesystem.FileSystemStorage{}, dir)
	if err != nil {
		t.Fatalf("can`t open a repo: %s", err.Error())
	}

	// Step 2: Hash objects and compare with real git
	files := []string{"README.md", "main.go", "lib/helper.go"}
	for _, f := range files {
		expectedSHA := runGit(t, "hash-object", "-w", f)

		content, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		actualSHA, err := repo.HashObject(content)
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
	actualTreeSHA, err := repo.WriteTree(".")
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
	actualCommitSHA, err := repo.CommitTree(commit)
	if err != nil {
		t.Fatal(err)
	}
	if expectedCommitSHA != actualCommitSHA {
		t.Errorf("FullWorkflow: CommitTree SHA mismatch:\n  expected: %s\n  actual:   %s", expectedCommitSHA, actualCommitSHA)
	}

	// Step 5: Update reference and verify
	ref := Reference{Name: "heads/main", Body: actualCommitSHA}
	if err := repo.UpdateReference(ref); err != nil {
		t.Fatal(err)
	}

	gitSHA := runGit(t, "rev-parse", "main")
	if gitSHA != actualCommitSHA {
		t.Errorf("FullWorkflow: branch ref mismatch:\n  expected: %s\n  actual:   %s", actualCommitSHA, gitSHA)
	}

	// Step 6: Create tag and verify
	if err := repo.CreateTag(Tag{"v1.0", actualCommitSHA}); err != nil {
		t.Fatal(err)
	}
	gitTagSHA := runGit(t, "rev-parse", "v1.0")
	if gitTagSHA != actualCommitSHA {
		t.Errorf("FullWorkflow: tag ref mismatch:\n  expected: %s\n  actual:   %s", actualCommitSHA, gitTagSHA)
	}
}
