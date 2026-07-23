package git

import (
	"fmt"
	"io"
	"os"
	"strings"
)

type Head struct {
	Ref string
}

func ReadHead() (Head, error) {
	path := "./.git/HEAD"
	file, err := os.Open(path)
	if err != nil {
		return Head{}, err
	}
	defer file.Close()

	content, err := io.ReadAll(file)
	if err != nil {
		return Head{}, err
	}

	ref, _ := strings.CutPrefix(string(content), "ref: ")

	return Head{Ref: ref}, nil
}

func UpdateHead(ref string) error {
	path := "./.git/HEAD"
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = file.WriteString(fmt.Sprintf("ref: %s", ref))
	if err != nil {
		return err
	}

	return nil
}

type Reference struct {
	Name string
	Sha1 string
}

func ListLocalBranches() ([]Reference, error) {
	path := "./.git/refs/heads"
	dir, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer dir.Close()

	// Read the directory recursively to get all branch references
	var branches []Reference
	files, err := listDirectory(path)
	if err != nil {
		return nil, err
	}

	for _, file := range files {
		ref, err := ReadReference(strings.TrimLeft(file, ".git/refs/"))
		if err != nil {
			return nil, err
		}
		branches = append(branches, ref)
	}

	return branches, nil
}

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

func UpdateReference(ref Reference) error {
	path := fmt.Sprintf(".git/refs/%s", ref.Name)
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = file.WriteString(ref.Sha1)
	if err != nil {
		return err
	}

	return nil
}

func ReadReference(name string) (Reference, error) {
	path := fmt.Sprintf(".git/refs/%s", name)
	file, err := os.Open(path)
	if err != nil {
		return Reference{}, err
	}
	defer file.Close()

	content, err := io.ReadAll(file)
	if err != nil {
		return Reference{}, err
	}
	
	sha := strings.ReplaceAll(string(content), "\n", "")

	return Reference{Name: name, Sha1: sha}, nil
}
