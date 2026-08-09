package filesystem

import (
	"fmt"
	"io"
	"os"
)

type FileSystemStorage struct {
	RepoPath string
	ObjectStorage
}

func (fs *FileSystemStorage) InitRepo(path, defaultBranch string) error {
	fs.RepoPath = path

	err := os.MkdirAll(fs.RepoPath+"/"+"./git", 0755)
	if err != nil {
		return fmt.Errorf("Error creating directory: %s\n", err)
	}

	err = fs.initRepoObjectStorage()
	if err != nil {
		return fmt.Errorf("Error creating object`s storage: %s\n", err)
	}
	err = fs.initRepoReferenceStorage(defaultBranch)
	if err != nil {
		return fmt.Errorf("Error creating reference`s storage: %s\n", err)
	}

	headFileContents := []byte("ref: refs/heads/main\n")
	if err := os.WriteFile(path+"/.git/HEAD", headFileContents, 0644); err != nil {
		return fmt.Errorf("Error writing file: %s\n", err)
	}

	return nil
}

func (fss *FileSystemStorage) initRepoObjectStorage() error {
	err := os.MkdirAll(fss.RepoPath+"/"+".git/objects", 0755)
	if err != nil {
		return fmt.Errorf("Error creating directory: %s\n", err)
	}
	return nil
}
func (fss *FileSystemStorage) initRepoReferenceStorage(defaultBranch string) error {
	err := os.MkdirAll(fss.RepoPath+"/"+".git/refs", 0755)
	if err != nil {
		return fmt.Errorf("Error creating directory: %s\n", err)
	}

	headFileContents := []byte("ref: refs/heads/" + defaultBranch + "\n")
	if err := os.WriteFile(fss.RepoPath+"/.git/HEAD", headFileContents, 0644); err != nil {
		return fmt.Errorf("Error writing file: %s\n", err)
	}
	return nil
}

type ObjectStorage struct {
}

type ReferenceStorage struct {
}

func (fos *ObjectStorage) objectPath(repoPath, sha string) string {
	return fmt.Sprintf("%s/.git/objects/%s/%s", repoPath, sha[:2], sha[2:])
}

func (fos *ObjectStorage) ObjectWriter(repoPath, sha1 string, size int64) (io.WriteCloser, error) {
	dir := fmt.Sprintf("%s/.git/objects/%s", repoPath, sha1[:2])
	err := os.MkdirAll(dir, os.ModePerm)
	if err != nil {
		return nil, fmt.Errorf("can`t create object directory: %s", err.Error())
	}

	file, err := os.Create(fos.objectPath(repoPath, sha1))
	if err != nil {
		return nil, fmt.Errorf("can`t create file: %s", err.Error())
	}
	return file, nil
}

func (fos *ObjectStorage) ObjectReader(repoPath, sha1 string) (io.ReadCloser, error) {
	return os.Open(fos.objectPath(repoPath, sha1))
}

func (fos *ObjectStorage) ObjectExists(repoPath, sha1 string) (bool, error) {
	_, err := os.Stat(fos.objectPath(repoPath, sha1))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, nil
}
