package git

import (
	"fmt"
	"got/storage/filesystem"
	"os"
	"testing"
)

// ============================================================================
// TestReadHead / TestUpdateHead
// ============================================================================

func TestReadHead(t *testing.T) {
	dir, cleanup := setupGitRepo(t)
	defer cleanup()

	repo, err := Open(&filesystem.FileSystemStorage{}, dir)
	if err != nil {
		t.Fatal(err)
	}

	// After git init, HEAD should point to main
	head, err := repo.ReadHead()
	if err != nil {
		t.Fatal(err)
	}

	expected := "refs/heads/main\n"
	if head.Ref != expected {
		t.Errorf("ReadHead mismatch:\n  expected: %q\n  actual:   %q", expected, head.Ref)
	}
}

func TestUpdateHead(t *testing.T) {
	dir, cleanup := setupGitRepo(t)
	defer cleanup()

	repo, err := Open(&filesystem.FileSystemStorage{}, dir)
	if err != nil {
		t.Fatal(err)
	}

	// Update HEAD using got
	newRef := "refs/heads/develop"
	if err := repo.UpdateHead(newRef); err != nil {
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
	dir, cleanup := setupGitRepo(t)
	defer cleanup()

	// Create a commit to have a real SHA
	createFile(t, "f.txt", "data\n")
	runGit(t, "add", ".")
	treeSHA := runGit(t, "write-tree")
	commitSHA := runGit(t, "commit-tree", treeSHA, "-m", "test")

	repo, err := Open(&filesystem.FileSystemStorage{}, dir)

	// Update reference using got
	ref := Reference{
		Name: "heads/my-branch",
		Body: commitSHA,
	}
	if err := repo.UpdateReference(ref); err != nil {
		t.Fatal(err)
	}

	// Read reference using got
	readRef, err := repo.ReadReference("heads/my-branch")
	if err != nil {
		t.Fatal(err)
	}

	if readRef.Name != ref.Name {
		t.Errorf("ReadReference name mismatch:\n  expected: %s\n  actual:   %s", ref.Name, readRef.Name)
	}
	if readRef.Body != ref.Body {
		t.Errorf("ReadReference SHA mismatch:\n  expected: %s\n  actual:   %s", ref.Body, readRef.Body)
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
	dir, cleanup := setupGitRepo(t)
	defer cleanup()

	// Create a commit
	createFile(t, "f.txt", "data\n")
	runGit(t, "add", ".")
	treeSHA := runGit(t, "write-tree")
	commitSHA := runGit(t, "commit-tree", treeSHA, "-m", "test")

	repo, err := Open(&filesystem.FileSystemStorage{}, dir)
	tagName := "v1.0"
	// Create tag using got
	if err := repo.CreateTag(Tag{tagName, commitSHA}); err != nil {
		t.Fatal(err)
	}

	// Read tag using got
	tag, err := repo.ReadTag(tagName)
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
	dir, cleanup := setupGitRepo(t)
	fmt.Println(dir)
	defer cleanup()

	// Create a commit
	createFile(t, "f.txt", "data\n")
	runGit(t, "add", ".")
	treeSHA := runGit(t, "write-tree")
	commitSHA := runGit(t, "commit-tree", treeSHA, "-m", "test")

	// Create annotated tag using real git
	runGit(t, "tag", "-a", "v1.0-annotated", "-m", "annotated tag message", commitSHA)

	repo, err := Open(&filesystem.FileSystemStorage{}, dir)
	// Read annotated tag using got
	tag, err := repo.ReadAnnotatedTag("v1.0-annotated")
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
// TestListLocalBranches
// ============================================================================

func TestListLocalBranches(t *testing.T) {
	dir, cleanup := setupGitRepo(t)
	defer cleanup()

	// Create a commit on main
	createFile(t, "f.txt", "data\n")
	runGit(t, "add", ".")
	treeSHA := runGit(t, "write-tree")
	commitSHA := runGit(t, "commit-tree", treeSHA, "-m", "test")
	runGit(t, "update-ref", "refs/heads/main", commitSHA)

	// Create another branch
	runGit(t, "branch", "feature-branch", commitSHA)

	repo, err := Open(&filesystem.FileSystemStorage{}, dir)
	// List branches using got
	branches, err := repo.ListLocalBranches()
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
	dir, cleanup := setupGitRepo(t)
	defer cleanup()

	// Create files
	createFile(t, "a.txt", "aaa\n")
	createFile(t, "b.txt", "bbb\n")
	os.MkdirAll("sub", 0755)
	createFile(t, "sub/c.txt", "ccc\n")

	// Stage and write tree with real git
	runGit(t, "add", ".")
	expectedTreeSHA := runGit(t, "write-tree")

	repo, err := Open(&filesystem.FileSystemStorage{}, dir)
	if err != nil {
		t.Fatalf("can`t open a repo: %s", err.Error())
	}
	// Write tree with got
	actualTreeSHA, err := repo.WriteTree(".")
	if err != nil {
		t.Fatal(err)
	}

	if expectedTreeSHA != actualTreeSHA {
		t.Errorf("WriteTree integration SHA mismatch:\n  expected: %s\n  actual:   %s", expectedTreeSHA, actualTreeSHA)
	}

	// Compare tree contents
	expectedNodes, err := repo.LsTree(expectedTreeSHA)
	if err != nil {
		t.Fatal(err)
	}
	actualNodes, err := repo.LsTree(actualTreeSHA)
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
