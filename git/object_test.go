package git

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"got/storage/filesystem"
	"os"
	"strings"
	"testing"
)

// ============================================================================
// TestHashObject
// ============================================================================

func TestHashObject(t *testing.T) {
	dir, cleanup := setupGitRepo(t)
	defer cleanup()

	// Create a test file
	content := "hello world\n"
	createFile(t, "test.txt", content)

	// Get SHA from real git
	expectedSHA := runGit(t, "hash-object", "-w", "test.txt")

	// Get SHA from got
	repo, err := Open(&filesystem.FileSystemStorage{}, dir)
	if err != nil {
		t.Fatalf("can`t open a repo: %s", err.Error())
	}
	actualSHA, err := repo.HashObject([]byte(content))
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
	repo, err := Open(&filesystem.FileSystemStorage{}, ".")
	if err != nil {
		t.Fatalf("can`t open a repo: %s", err.Error())
	}
	actualSHA, err := repo.HashObject([]byte(""))
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
	repo, err := Open(&filesystem.FileSystemStorage{}, ".")
	if err != nil {
		t.Fatalf("can`t open a repo: %s", err.Error())
	}
	actualSHA, err := repo.HashObject(content)
	if err != nil {
		t.Fatal(err)
	}

	if expectedSHA != actualSHA {
		t.Errorf("HashObject (binary) SHA mismatch:\n  expected: %s\n  actual:   %s", expectedSHA, actualSHA)
	}
}

func TestHashObject_ExistingDifferentObjectCollision(t *testing.T) {
	dir, cleanup := setupGitRepo(t)
	defer cleanup()

	content := "hello world\n"
	createFile(t, "test.txt", content)

	file, err := os.ReadFile("test.txt")
	if err != nil {
		t.Errorf("can`t read a file: %s", err.Error())
	}
	repo, err := Open(&filesystem.FileSystemStorage{}, dir)
	if err != nil {
		t.Fatalf("can`t open a repo: %s", err.Error())
	}
	sha, err := repo.HashObject(file)
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
	_, err = repo.HashObject(file)
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
	dir, cleanup := setupGitRepo(t)
	defer cleanup()

	content := "hello world\n"
	createFile(t, "test.txt", content)

	sha := runGit(t, "hash-object", "-w", "test.txt")

	// Get content from real git (pretty-print)
	expectedContent := runGit(t, "cat-file", "-p", sha)

	repo, err := Open(&filesystem.FileSystemStorage{}, dir)
	if err != nil {
		t.Fatalf("can`t open a repo: %s", err.Error())
	}
	// Get content from got
	gotRaw := repo.CatFile(sha)

	if expectedContent != gotRaw {
		t.Errorf("CatFile content mismatch:\n  expected: %q\n  actual:   %q", expectedContent, gotRaw)
	}
}

// ============================================================================
// TestLsTree
// ============================================================================

func TestLsTree(t *testing.T) {
	dir, cleanup := setupGitRepo(t)
	defer cleanup()

	// Create files and stage them
	createFile(t, "file1.txt", "content1\n")
	createFile(t, "file2.txt", "content2\n")

	runGit(t, "add", "file1.txt", "file2.txt")

	// Get tree SHA from real git
	treeSHA := runGit(t, "write-tree")

	// Get tree listing from real git
	expectedOutput := runGit(t, "ls-tree", treeSHA)

	repo, err := Open(&filesystem.FileSystemStorage{}, dir)
	if err != nil {
		t.Fatalf("can`t open a repo: %s", err.Error())
	}
	// Get tree listing from got
	nodes, err := repo.LsTree(treeSHA)
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
	dir, cleanup := setupGitRepo(t)
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

	repo, err := Open(&filesystem.FileSystemStorage{}, dir)
	if err != nil {
		t.Fatalf("can`t open a repo: %s", err.Error())
	}
	// Get tree listing from got
	nodes, err := repo.LsTree(treeSHA)
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
	dir, cleanup := setupGitRepo(t)
	defer cleanup()

	createFile(t, "a.txt", "aaa\n")
	createFile(t, "b.txt", "bbb\n")

	runGit(t, "add", ".")

	// Get tree SHA from real git
	expectedSHA := runGit(t, "write-tree")

	repo, err := Open(&filesystem.FileSystemStorage{}, dir)
	if err != nil {
		t.Fatalf("can`t open a repo: %s", err.Error())
	}
	// Get tree SHA from got
	actualSHA, err := repo.WriteTree(".")
	if err != nil {
		t.Fatal(err)
	}

	if expectedSHA != actualSHA {
		t.Errorf("WriteTree SHA mismatch:\n  expected: %s\n  actual:   %s", expectedSHA, actualSHA)
	}
}

func TestWriteTree_WithSubdirectory(t *testing.T) {
	dir, cleanup := setupGitRepo(t)
	defer cleanup()

	createFile(t, "root.txt", "root\n")
	os.MkdirAll("sub", 0755)
	createFile(t, "sub/nested.txt", "nested\n")

	runGit(t, "add", ".")

	expectedSHA := runGit(t, "write-tree")
	repo, err := Open(&filesystem.FileSystemStorage{}, dir)
	if err != nil {
		t.Fatalf("can`t open a repo: %s", err.Error())
	}
	actualSHA, err := repo.WriteTree(".")
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
	dir, cleanup := setupGitRepo(t)
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

	repo, err := Open(&filesystem.FileSystemStorage{}, dir)
	if err != nil {
		t.Fatalf("can`t open a repo: %s", err.Error())
	}
	actualSHA, err := repo.CommitTree(commit)
	if err != nil {
		t.Fatal(err)
	}

	if expectedSHA != actualSHA {
		t.Errorf("CommitTree SHA mismatch:\n  expected: %s\n  actual:   %s", expectedSHA, actualSHA)
	}
}

func TestCommitTree_WithParent(t *testing.T) {
	dir, cleanup := setupGitRepo(t)
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

	repo, err := Open(&filesystem.FileSystemStorage{}, dir)
	if err != nil {
		t.Fatalf("can`t open a repo: %s", err.Error())
	}
	actualSHA, err := repo.CommitTree(commit)
	if err != nil {
		t.Fatal(err)
	}

	if expectedSHA != actualSHA {
		t.Errorf("CommitTree (with parent) SHA mismatch:\n  expected: %s\n  actual:   %s", expectedSHA, actualSHA)
	}
}