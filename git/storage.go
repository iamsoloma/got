package git

import "io"

// Storage abstracts the git object and ref storage layer.
// All package-level functions delegate to DefaultStorage.
type Storage interface {
	// InitDirs creates the required .git directory structure and initial HEAD file.
	InitDirs() error

	// ReadObject returns a reader for the raw (zlib-compressed) content of the
	// object identified by sha. The caller is responsible for closing the reader.
	ReadObject(sha string) (io.ReadCloser, error)

	// WriteObject stores the raw (zlib-compressed) bytes for the given sha.
	WriteObject(sha string, compressed []byte) error

	// StatObject reports whether an object with the given sha already exists.
	StatObject(sha string) (bool, error)

	// ReadHead returns the raw content of the HEAD file.
	ReadHead() ([]byte, error)

	// WriteHead replaces the content of the HEAD file.
	WriteHead(content []byte) error

	// ReadRef returns the raw content of the ref file identified by name
	// (relative to .git/refs/, e.g. "heads/main").
	ReadRef(name string) ([]byte, error)

	// WriteRef writes content to the ref file identified by name
	// (relative to .git/refs/, e.g. "heads/main").
	WriteRef(name string, content []byte) error

	// ListRefs recursively lists all ref file names under dir
	// (relative to .git/refs/, e.g. "heads").
	// Each returned name is also relative to .git/refs/.
	ListRefs(dir string) ([]string, error)
}

// DefaultStorage is the storage backend used by all package-level git functions.
// It can be replaced in tests or other contexts to swap out the storage layer.
var DefaultStorage Storage = &FSStorage{}
