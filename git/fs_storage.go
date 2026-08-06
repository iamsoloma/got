package git

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// FSStorage is a filesystem-backed Storage implementation that uses the
// standard .git directory layout relative to the current working directory.
type FSStorage struct{}

// InitDirs creates the .git directory structure and writes the initial HEAD.
func (s *FSStorage) InitDirs() error {
	for _, dir := range []string{".git", ".git/objects", ".git/refs"} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("error creating directory %s: %w", dir, err)
		}
	}
	headFileContents := []byte("ref: refs/heads/main\n")
	if err := os.WriteFile(".git/HEAD", headFileContents, 0644); err != nil {
		return fmt.Errorf("error writing HEAD: %w", err)
	}
	return nil
}

func (s *FSStorage) objectPath(sha string) string {
	return fmt.Sprintf(".git/objects/%s/%s", sha[:2], sha[2:])
}

// ReadObject opens the compressed object file for the given sha.
func (s *FSStorage) ReadObject(sha string) (io.ReadCloser, error) {
	return os.Open(s.objectPath(sha))
}

// WriteObject stores the compressed bytes under the standard object path.
func (s *FSStorage) WriteObject(sha string, compressed []byte) error {
	path := s.objectPath(sha)
	if err := os.MkdirAll(filepath.Dir(path), os.ModePerm); err != nil {
		return fmt.Errorf("can't create subdirs: %w", err)
	}
	return os.WriteFile(path, compressed, 0644)
}

// StatObject reports whether the object file for sha already exists.
func (s *FSStorage) StatObject(sha string) (bool, error) {
	_, err := os.Stat(s.objectPath(sha))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// ReadHead returns the raw content of .git/HEAD.
func (s *FSStorage) ReadHead() ([]byte, error) {
	return os.ReadFile(".git/HEAD")
}

// WriteHead writes content to .git/HEAD.
func (s *FSStorage) WriteHead(content []byte) error {
	return os.WriteFile(".git/HEAD", content, 0644)
}

// ReadRef returns the raw content of the ref file at .git/refs/<name>.
func (s *FSStorage) ReadRef(name string) ([]byte, error) {
	return os.ReadFile(fmt.Sprintf(".git/refs/%s", name))
}

// WriteRef writes content to the ref file at .git/refs/<name>,
// creating any intermediate directories as needed.
func (s *FSStorage) WriteRef(name string, content []byte) error {
	path := fmt.Sprintf(".git/refs/%s", name)
	if err := os.MkdirAll(filepath.Dir(path), os.ModePerm); err != nil {
		return err
	}
	return os.WriteFile(path, content, 0644)
}

// ListRefs recursively lists all ref file names under .git/refs/<dir>.
// Each returned name is relative to .git/refs/ (e.g. "heads/main").
func (s *FSStorage) ListRefs(dir string) ([]string, error) {
	base := fmt.Sprintf(".git/refs/%s", dir)
	paths, err := listRefFiles(base)
	if err != nil {
		return nil, err
	}
	prefix := ".git/refs/"
	names := make([]string, 0, len(paths))
	for _, p := range paths {
		names = append(names, strings.TrimPrefix(p, prefix))
	}
	return names, nil
}

// listRefFiles recursively collects all file paths under path.
func listRefFiles(path string) ([]string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, entry := range entries {
		fullPath := fmt.Sprintf("%s/%s", path, entry.Name())
		if entry.IsDir() {
			sub, err := listRefFiles(fullPath)
			if err != nil {
				return nil, err
			}
			files = append(files, sub...)
		} else {
			files = append(files, fullPath)
		}
	}
	return files, nil
}
