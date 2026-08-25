package storage

import (
	"io"
)

type Storage interface {
	InitRepo(path string, defaultBranch string) error
	OpenRepo(path string)
	ObjectStorage
	ReferenceStorage
}

type ObjectStorage interface {
	ObjectWriter(sha1 string, size int64) (io.WriteCloser, error)
	ObjectReader(sha1 string) (io.ReadCloser, error)
	ObjectExists(sha1 string) (bool, error)
}

type ReferenceStorage interface {
	SetHEAD(body string) error
	GetHEAD() (body string, err error)
	SetReference(name string, body string) error
	GetReference(name string) (body string, err error)
	ListReferences() (names []string, err error)
	DeleteReference(name string) error
}
