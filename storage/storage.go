package storage

import (
	"io"
)

type Storage interface {
	InitRepo(path string, defaultBranch string) error
	ObjectStorage
	//ReferenceStorage
}

type ObjectStorage interface {
	ObjectWriter(repoPath, sha1 string, size int64) (io.WriteCloser, error)
	ObjectReader(repoPath,sha1 string) (io.ReadCloser, error)
	ObjectExists(repoPath, sha1 string) (bool, error)
}

type ReferenceStorage interface {
	SetReference(repoPath, name string, sha1 string) error
	GetReference(repoPath, name string) (string, error)
	ListReferences(repoPath string) ([]string, error)
	DeleteReference(repoPath, name string) error
}
