package git

import (
	"got/storage"
)

type Repository struct {
	Storage storage.Storage
	path    string
}

func Init(s storage.Storage, path, defaultBranch string) (repo *Repository, err error) {
	repo = &Repository{Storage: s}
	err = repo.Storage.InitRepo(path, defaultBranch)

	return repo, err
}

func Open(s storage.Storage, path string) (repo *Repository, err error) {
	repo = &Repository{Storage: s, path: path}
	return repo, err
}