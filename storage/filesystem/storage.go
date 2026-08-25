package filesystem

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type FileSystemStorage struct {
	RepoPath string
	ObjectStorage
	ReferenceStorage
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

type ReferenceStorage struct {
}

func normalizeRefName(refName string) string {
	refName = strings.TrimPrefix(refName, "/")
	refName = strings.TrimPrefix(refName, "refs/")
	return filepath.Clean(refName)
}

func (frs *ReferenceStorage) SetHEAD(repoPath, body string) error {
	path := repoPath + "/.git/HEAD"
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = file.WriteString(body)
	if err != nil {
		return err
	}

	return nil
}

func (frs *ReferenceStorage) GetHEAD(repoPath string) (body string, err error) {
	path := repoPath + "/.git/HEAD"
	file, err := os.Open(path)
	if err != nil {
		return body, err
	}
	defer file.Close()

	content, err := io.ReadAll(file)
	if err != nil {
		return body, err
	}

	body = string(content)

	return body, nil
}

func (frs *ReferenceStorage) SetReference(repoPath, refName string, body string) error {
	//path := fmt.Sprintf(repoPath+"/.git/refs/%s", refName)
	refName = normalizeRefName(refName)
	path := filepath.Join(repoPath, ".git", "refs", refName)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = file.WriteString(body)
	if err != nil {
		return err
	}

	return nil
}
func (frs *ReferenceStorage) GetReference(repoPath, name string) (body string, err error) {
	//path := fmt.Sprintf(repoPath+"/.git/refs/%s", name)
	name = normalizeRefName(name)
	path := filepath.Join(repoPath, ".git", "refs", name)
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	content, err := io.ReadAll(file)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(content)), nil

}
func (frs *ReferenceStorage) ListReferences(repoPath string) (names []string, err error) {
	//path := repoPath + "/.git/refs"
	path := filepath.Join(repoPath, ".git", "refs")
	dir, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer dir.Close()

	files, err := listDirectory(path)
	if err != nil {
		return nil, err
	}

	for _, file := range files {
		//name := strings.TrimLeft(file, ".git/refs/")
		relPath, err := filepath.Rel(path, file)
		if err != nil {
			return nil, err
		}
		name := filepath.ToSlash(relPath)
		if name == "." {
			continue
		}
		names = append(names, name)
	}

	return names, nil

}
func (frs *ReferenceStorage) DeleteReference(repoPath, name string) error {
	path := repoPath + "/.git/refs/" + name
	return os.Remove(path)

}

// Read the directory recursively to get all file`s paths
func listDirectory(path string) (files []string, err error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			subDir := fmt.Sprintf("%s/%s", path, entry.Name())
			subFiles, err := listDirectory(subDir)
			if err != nil {
				return nil, err
			}
			files = append(files, subFiles...)
		} else {
			files = append(files, fmt.Sprintf("%s/%s", path, entry.Name()))
		}
	}

	return files, nil

}
