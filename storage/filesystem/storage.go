package filesystem

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type FileSystemStorage struct {
	repoPath string
	ObjectStorage
	PackStorage
	ReferenceStorage
}

func (fs *FileSystemStorage) InitRepo(path, defaultBranch string) error {
	repoPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve repository path: %w", err)
	}
	fs.repoPath = repoPath

	if err := os.MkdirAll(fs.repoPath, 0755); err != nil {
		return fmt.Errorf("create repository directory: %w", err)
	}
	if err := fs.initRepoObjectStorage(fs.repoPath); err != nil {
		return fmt.Errorf("create object storage: %w", err)
	}
	if err := fs.initRepoPackStorage(fs.repoPath); err != nil {
		return fmt.Errorf("create pack storage: %w", err)
	}
	if err := fs.initRepoReferenceStorage(fs.repoPath, defaultBranch); err != nil {
		return fmt.Errorf("create reference storage: %w", err)
	}
	config := []byte("[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n\tbare = true\n")
	if err := os.WriteFile(filepath.Join(fs.repoPath, "config"), config, 0644); err != nil {
		return fmt.Errorf("write repository config: %w", err)
	}

	return nil
}

func (fss *FileSystemStorage) initRepoObjectStorage(path string) error {
	fss.ObjectStorage.repoPath = path
	return os.MkdirAll(filepath.Join(path, "objects"), 0755)
}

func (fss *FileSystemStorage) initRepoPackStorage(path string) error {
	fss.PackStorage.repoPath = path
	return os.MkdirAll(filepath.Join(path, "objects", "pack"), 0755)
}

func (fss *FileSystemStorage) initRepoReferenceStorage(path, defaultBranch string) error {
	fss.ReferenceStorage.repoPath = path
	for _, dir := range []string{"heads", "tags"} {
		if err := os.MkdirAll(filepath.Join(path, "refs", dir), 0755); err != nil {
			return err
		}
	}

	headFileContents := []byte("ref: refs/heads/" + defaultBranch + "\n")
	if err := os.WriteFile(filepath.Join(path, "HEAD"), headFileContents, 0644); err != nil {
		return fmt.Errorf("write HEAD: %w", err)
	}
	return nil
}

func (fs *FileSystemStorage) OpenRepo(path string) error {
	repoPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve repository path: %w", err)
	}
	for _, name := range []string{"HEAD", "objects", "refs"} {
		info, err := os.Stat(filepath.Join(repoPath, name))
		if err != nil {
			return fmt.Errorf("open repository: %s: %w", name, err)
		}
		if name == "HEAD" && !info.Mode().IsRegular() {
			return fmt.Errorf("open repository: HEAD is not a regular file")
		}
		if name != "HEAD" && !info.IsDir() {
			return fmt.Errorf("open repository: %s is not a directory", name)
		}
	}

	fs.repoPath = repoPath
	fs.ObjectStorage.repoPath = repoPath
	fs.PackStorage.repoPath = repoPath
	fs.ReferenceStorage.repoPath = repoPath
	return nil
}

type ObjectStorage struct {
	repoPath string
}

func (fos *ObjectStorage) objectPath(sha string) string {
	return filepath.Join(fos.repoPath, "objects", sha[:2], sha[2:])
}

func (fos *ObjectStorage) ObjectWriter(sha1 string, size int64) (io.WriteCloser, error) {
	dir := filepath.Join(fos.repoPath, "objects", sha1[:2])
	err := os.MkdirAll(dir, os.ModePerm)
	if err != nil {
		return nil, fmt.Errorf("can`t create object directory: %s", err.Error())
	}

	file, err := os.Create(fos.objectPath(sha1))
	if err != nil {
		return nil, fmt.Errorf("can`t create file: %s", err.Error())
	}
	return file, nil
}

func (fos *ObjectStorage) ObjectReader(sha1 string) (io.ReadCloser, error) {
	return os.Open(fos.objectPath(sha1))
}

func (fos *ObjectStorage) ObjectExists(sha1 string) (bool, error) {
	_, err := os.Stat(fos.objectPath(sha1))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func (fos *ObjectStorage) ListObjects() ([]string, error) {
	var objects []string
	objectsDir := filepath.Join(fos.repoPath, "objects")
	err := filepath.Walk(objectsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && len(info.Name()) == 38 { // SHA-1 hash length is 40 characters (2 for directory + 38 for file)
			sha1 := filepath.Base(filepath.Dir(path)) + info.Name()
			objects = append(objects, sha1)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return objects, nil
}

type PackStorage struct {
	repoPath string
}

func (fps *PackStorage) PackWriter(packName string, size int64) (io.WriteCloser, error) {
	dir := fmt.Sprintf("%s/objects/pack", fps.repoPath)
	err := os.MkdirAll(dir, os.ModePerm)
	if err != nil {
		return nil, fmt.Errorf("can`t create pack directory: %s", err.Error())
	}

	file, err := os.Create(filepath.Join(dir, packName))
	if err != nil {
		return nil, fmt.Errorf("can`t create pack file: %s", err.Error())
	}
	return file, nil
}

func (fps *PackStorage) PackReader(packName string) (io.ReadCloser, error) {
	return os.Open(filepath.Join(fps.repoPath, "objects", "pack", packName))
}

func (fps *PackStorage) PackExists(packName string) (bool, error) {
	_, err := os.Stat(filepath.Join(fps.repoPath, "objects", "pack", packName))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func (fps *PackStorage) ListPacks() ([]string, error) {
	var packs []string
	packsDir := filepath.Join(fps.repoPath, "objects", "pack")
	err := filepath.Walk(packsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(info.Name(), ".pack") {
			packName := info.Name()
			packs = append(packs, packName)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return packs, nil
}

type ReferenceStorage struct {
	repoPath string
}

func normalizeRefName(refName string) string {
	refName = strings.TrimPrefix(refName, "/")
	refName = strings.TrimPrefix(refName, "refs/")
	return filepath.Clean(refName)
}

func (frs *ReferenceStorage) SetHEAD(body string) error {
	path := filepath.Join(frs.repoPath, "HEAD")
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

func (frs *ReferenceStorage) GetHEAD() (body string, err error) {
	path := filepath.Join(frs.repoPath, "HEAD")
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

func (frs *ReferenceStorage) SetReference(refName string, body string) error {
	refName = normalizeRefName(refName)
	path := filepath.Join(frs.repoPath, "refs", refName)
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
func (frs *ReferenceStorage) GetReference(name string) (body string, err error) {
	name = normalizeRefName(name)
	path := filepath.Join(frs.repoPath, "refs", name)
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

func (frs *ReferenceStorage) ListReferences() (names []string, err error) {
	path := filepath.Join(frs.repoPath, "refs")
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

func (frs *ReferenceStorage) DeleteReference(name string) error {
	path := filepath.Join(frs.repoPath, "refs", normalizeRefName(name))
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
