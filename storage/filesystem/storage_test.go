package filesystem

import (
	"errors"
	"got/storage"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

const (
	sha    = "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391"
	altSha = "0123456789abcdef0123456789abcdef01234567"
)

// compile-time check that the filesystem implementation satisfies the interface
var _ storage.Storage = (*FileSystemStorage)(nil)

// newStorage returns a FileSystemStorage with an initialized repo in a temp dir.
func newStorage(t *testing.T) (*FileSystemStorage, string) {
	t.Helper()

	dir := t.TempDir()
	fss := &FileSystemStorage{}
	if err := fss.InitRepo(dir, "main"); err != nil {
		t.Fatalf("InitRepo failed: %s", err)
	}
	return fss, dir
}

// writeObject writes content as an object and returns nothing, failing on error.
func writeObject(t *testing.T, fss *FileSystemStorage, sha1, content string) {
	t.Helper()

	w, err := fss.ObjectWriter(sha1, int64(len(content)))
	if err != nil {
		t.Fatalf("ObjectWriter failed: %s", err)
	}
	if _, err := io.WriteString(w, content); err != nil {
		t.Fatalf("writing object failed: %s", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("closing object failed: %s", err)
	}
}

// ============================================================================
// InitRepo / OpenRepo
// ============================================================================

func TestInitRepo(t *testing.T) {
	_, dir := newStorage(t)

	for _, d := range []string{".git", ".git/objects", ".git/refs"} {
		info, err := os.Stat(filepath.Join(dir, d))
		if err != nil {
			t.Fatalf("InitRepo: expected %s to exist: %s", d, err)
		}
		if !info.IsDir() {
			t.Errorf("InitRepo: %s is not a directory", d)
		}
	}

	head, err := os.ReadFile(filepath.Join(dir, ".git", "HEAD"))
	if err != nil {
		t.Fatal(err)
	}
	expected := "ref: refs/heads/main\n"
	if string(head) != expected {
		t.Errorf("InitRepo: HEAD mismatch:\n  expected: %q\n  actual:   %q", expected, string(head))
	}
}

func TestInitRepoIsIdempotent(t *testing.T) {
	fss, dir := newStorage(t)

	if err := fss.SetReference("heads/main", altSha); err != nil {
		t.Fatal(err)
	}
	if err := fss.InitRepo(dir, "main"); err != nil {
		t.Fatalf("second InitRepo failed: %s", err)
	}

	body, err := fss.GetReference("heads/main")
	if err != nil {
		t.Fatalf("GetReference after re-init failed: %s", err)
	}
	if body != altSha {
		t.Errorf("InitRepo: existing reference was lost:\n  expected: %s\n  actual:   %s", altSha, body)
	}
}

func TestOpenRepoPropagatesPath(t *testing.T) {
	_, dir := newStorage(t)

	opened := &FileSystemStorage{}
	opened.OpenRepo(dir)

	if err := opened.SetReference("heads/main", sha); err != nil {
		t.Fatalf("SetReference after OpenRepo failed: %s", err)
	}
	body, err := opened.GetReference("heads/main")
	if err != nil {
		t.Fatalf("GetReference after OpenRepo failed: %s", err)
	}
	if body != sha {
		t.Errorf("OpenRepo: reference mismatch:\n  expected: %s\n  actual:   %s", sha, body)
	}

	writeObject(t, opened, sha, "blob")
	exists, err := opened.ObjectExists(sha)
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Error("OpenRepo: object written after OpenRepo does not exist")
	}
}

// ============================================================================
// ObjectStorage
// ============================================================================

func TestObjectWriteAndRead(t *testing.T) {
	fss, dir := newStorage(t)

	content := "blob 12\x00hello world!"
	writeObject(t, fss, sha, content)

	// Object must be stored using git`s fan-out layout
	path := filepath.Join(dir, ".git", "objects", sha[:2], sha[2:])
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("ObjectWriter: expected object at %s: %s", path, err)
	}

	r, err := fss.ObjectReader(sha)
	if err != nil {
		t.Fatalf("ObjectReader failed: %s", err)
	}
	defer r.Close()

	actual, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(actual) != content {
		t.Errorf("ObjectReader mismatch:\n  expected: %q\n  actual:   %q", content, string(actual))
	}
}

func TestObjectWriterOverwrites(t *testing.T) {
	fss, _ := newStorage(t)

	writeObject(t, fss, sha, "first content")
	writeObject(t, fss, sha, "second")

	r, err := fss.ObjectReader(sha)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	actual, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(actual) != "second" {
		t.Errorf("ObjectWriter: expected object to be truncated:\n  expected: %q\n  actual:   %q", "second", string(actual))
	}
}

func TestObjectReaderMissing(t *testing.T) {
	fss, _ := newStorage(t)

	if _, err := fss.ObjectReader(sha); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("ObjectReader: expected os.ErrNotExist, actual: %v", err)
	}
}

func TestObjectExists(t *testing.T) {
	fss, _ := newStorage(t)

	exists, err := fss.ObjectExists(sha)
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Error("ObjectExists: expected false for missing object")
	}

	writeObject(t, fss, sha, "content")

	exists, err = fss.ObjectExists(sha)
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Error("ObjectExists: expected true for existing object")
	}
}

// ============================================================================
// ReferenceStorage: HEAD
// ============================================================================

func TestSetAndGetHEAD(t *testing.T) {
	fss, _ := newStorage(t)

	body := "ref: refs/heads/develop\n"
	if err := fss.SetHEAD(body); err != nil {
		t.Fatalf("SetHEAD failed: %s", err)
	}

	actual, err := fss.GetHEAD()
	if err != nil {
		t.Fatalf("GetHEAD failed: %s", err)
	}
	if actual != body {
		t.Errorf("GetHEAD mismatch:\n  expected: %q\n  actual:   %q", body, actual)
	}
}

func TestSetHEADOverwrites(t *testing.T) {
	fss, _ := newStorage(t)

	if err := fss.SetHEAD("ref: refs/heads/some-very-long-branch-name\n"); err != nil {
		t.Fatal(err)
	}
	if err := fss.SetHEAD("ref: refs/heads/x\n"); err != nil {
		t.Fatal(err)
	}

	actual, err := fss.GetHEAD()
	if err != nil {
		t.Fatal(err)
	}
	expected := "ref: refs/heads/x\n"
	if actual != expected {
		t.Errorf("SetHEAD: expected HEAD to be truncated:\n  expected: %q\n  actual:   %q", expected, actual)
	}
}

func TestGetHEADMissing(t *testing.T) {
	fss := &FileSystemStorage{}
	fss.OpenRepo(t.TempDir())

	if _, err := fss.GetHEAD(); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("GetHEAD: expected os.ErrNotExist, actual: %v", err)
	}
}

// ============================================================================
// ReferenceStorage: references
// ============================================================================

func TestSetAndGetReference(t *testing.T) {
	fss, dir := newStorage(t)

	if err := fss.SetReference("heads/feature/nested", sha); err != nil {
		t.Fatalf("SetReference failed: %s", err)
	}

	path := filepath.Join(dir, ".git", "refs", "heads", "feature", "nested")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("SetReference: expected ref file at %s: %s", path, err)
	}

	body, err := fss.GetReference("heads/feature/nested")
	if err != nil {
		t.Fatalf("GetReference failed: %s", err)
	}
	if body != sha {
		t.Errorf("GetReference mismatch:\n  expected: %s\n  actual:   %s", sha, body)
	}
}

func TestReferenceNameNormalization(t *testing.T) {
	fss, _ := newStorage(t)

	if err := fss.SetReference("/refs/heads/main", sha); err != nil {
		t.Fatalf("SetReference failed: %s", err)
	}

	// All these spellings must resolve to the same reference
	for _, name := range []string{"heads/main", "refs/heads/main", "/refs/heads/main", "heads/./main"} {
		body, err := fss.GetReference(name)
		if err != nil {
			t.Fatalf("GetReference(%q) failed: %s", name, err)
		}
		if body != sha {
			t.Errorf("GetReference(%q) mismatch:\n  expected: %s\n  actual:   %s", name, sha, body)
		}
	}
}

func TestGetReferenceTrimsWhitespace(t *testing.T) {
	fss, _ := newStorage(t)

	if err := fss.SetReference("heads/main", sha+"\n"); err != nil {
		t.Fatal(err)
	}

	body, err := fss.GetReference("heads/main")
	if err != nil {
		t.Fatal(err)
	}
	if body != sha {
		t.Errorf("GetReference: expected trimmed body:\n  expected: %q\n  actual:   %q", sha, body)
	}
}

func TestSetReferenceOverwrites(t *testing.T) {
	fss, _ := newStorage(t)

	if err := fss.SetReference("heads/main", sha); err != nil {
		t.Fatal(err)
	}
	if err := fss.SetReference("heads/main", altSha); err != nil {
		t.Fatal(err)
	}

	body, err := fss.GetReference("heads/main")
	if err != nil {
		t.Fatal(err)
	}
	if body != altSha {
		t.Errorf("SetReference: expected reference to be updated:\n  expected: %s\n  actual:   %s", altSha, body)
	}
}

func TestGetReferenceMissing(t *testing.T) {
	fss, _ := newStorage(t)

	if _, err := fss.GetReference("heads/nonexistent"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("GetReference: expected os.ErrNotExist, actual: %v", err)
	}
}

func TestListReferences(t *testing.T) {
	fss, _ := newStorage(t)

	refs := map[string]string{
		"heads/main":           sha,
		"heads/feature/nested": altSha,
		"tags/v1.0":            sha,
	}
	for name, body := range refs {
		if err := fss.SetReference(name, body); err != nil {
			t.Fatal(err)
		}
	}

	names, err := fss.ListReferences()
	if err != nil {
		t.Fatalf("ListReferences failed: %s", err)
	}
	sort.Strings(names)

	expected := []string{"heads/feature/nested", "heads/main", "tags/v1.0"}
	if len(names) != len(expected) {
		t.Fatalf("ListReferences mismatch:\n  expected: %v\n  actual:   %v", expected, names)
	}
	for i, name := range expected {
		if names[i] != name {
			t.Errorf("ListReferences mismatch:\n  expected: %v\n  actual:   %v", expected, names)
			break
		}
	}
}

func TestListReferencesEmpty(t *testing.T) {
	fss, _ := newStorage(t)

	names, err := fss.ListReferences()
	if err != nil {
		t.Fatalf("ListReferences failed: %s", err)
	}
	if len(names) != 0 {
		t.Errorf("ListReferences: expected no references, actual: %v", names)
	}
}

func TestListReferencesMissingDir(t *testing.T) {
	fss := &FileSystemStorage{}
	fss.OpenRepo(t.TempDir())

	if _, err := fss.ListReferences(); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("ListReferences: expected os.ErrNotExist, actual: %v", err)
	}
}

func TestDeleteReference(t *testing.T) {
	fss, _ := newStorage(t)

	if err := fss.SetReference("heads/main", sha); err != nil {
		t.Fatal(err)
	}
	if err := fss.DeleteReference("heads/main"); err != nil {
		t.Fatalf("DeleteReference failed: %s", err)
	}

	if _, err := fss.GetReference("heads/main"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("DeleteReference: expected reference to be gone, actual error: %v", err)
	}

	names, err := fss.ListReferences()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name == "heads/main" {
			t.Error("DeleteReference: deleted reference is still listed")
		}
	}
}

func TestDeleteReferenceMissing(t *testing.T) {
	fss, _ := newStorage(t)

	if err := fss.DeleteReference("heads/nonexistent"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("DeleteReference: expected os.ErrNotExist, actual: %v", err)
	}
}

// ============================================================================
// listDirectory
// ============================================================================

func TestListDirectoryRecursive(t *testing.T) {
	dir := t.TempDir()

	if err := os.MkdirAll(filepath.Join(dir, "a", "b"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"root.txt", "a/one.txt", "a/b/two.txt"} {
		if err := os.WriteFile(filepath.Join(dir, path), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	files, err := listDirectory(dir)
	if err != nil {
		t.Fatalf("listDirectory failed: %s", err)
	}
	if len(files) != 3 {
		t.Fatalf("listDirectory: expected 3 files, actual: %v", files)
	}

	found := map[string]bool{}
	for _, file := range files {
		rel, err := filepath.Rel(dir, file)
		if err != nil {
			t.Fatal(err)
		}
		found[filepath.ToSlash(rel)] = true
	}
	for _, path := range []string{"root.txt", "a/one.txt", "a/b/two.txt"} {
		if !found[path] {
			t.Errorf("listDirectory: missing %s in %v", path, files)
		}
	}
}

func TestListDirectoryMissing(t *testing.T) {
	if _, err := listDirectory(filepath.Join(t.TempDir(), "nope")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("listDirectory: expected os.ErrNotExist, actual: %v", err)
	}
}
