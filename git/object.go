package git

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"got/utils"
	"strings"

	"io"
	"os"
	"sort"
	"strconv"
)

// Read the contents of a file in the object storage and return it as a string
func (r *Repository) CatFile(objectSha string) string {
	file, err := r.Storage.ObjectReader(objectSha)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s", err.Error())
	}

	reader, err := zlib.NewReader(file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s", err.Error())
	}

	s, err := io.ReadAll(reader)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s", err.Error())
	}

	parts := strings.Split(string(s), "\x00")
	res, _ := strings.CutSuffix(parts[1], "\n")

	reader.Close()

	return res
}

// Write blob object
func (r *Repository) HashObject(content []byte) (sha string, err error) {
	return r.WriteObject(content, "blob")
}

func (r *Repository) WriteObject(content []byte, objectType string) (sha string, err error) {
	object := fmt.Sprintf("%s %d\x00%s", objectType, len(content), content)
	sha = fmt.Sprintf("%x", sha1.Sum([]byte(object)))

	// Object exists?
	exists, err := r.Storage.ObjectExists(sha)
	if err != nil {
		return sha, fmt.Errorf("can`t stat object file: %s", err.Error())
	}

	if !exists {
		file, err := r.Storage.ObjectWriter(sha, int64(len(object)))
		if err != nil {
			return sha, fmt.Errorf("can`t create an object: %s", err.Error())
		}
		defer file.Close()

		writer := zlib.NewWriter(file)
		defer writer.Close()
		_, err = writer.Write([]byte(object))
		if err != nil {
			return sha, fmt.Errorf("can`t write in object: %s", err.Error())
		}

		return sha, nil
	}

	// Yes, read it
	file, err := r.Storage.ObjectReader(sha)
	if err != nil {
		return sha, fmt.Errorf("can`t open existing object: %s", err.Error())
	}
	defer file.Close()

	reader, err := zlib.NewReader(file)
	if err != nil {
		return sha, fmt.Errorf("can`t read existing object: %s", err.Error())
	}
	existing, err := io.ReadAll(reader)
	reader.Close()
	if err != nil {
		return sha, fmt.Errorf("can`t read existing object: %s", err.Error())
	}

	// Comparing the contents of the current object with the new one
	if !bytes.Equal(existing, []byte(object)) {
		return sha, fmt.Errorf("hash collision: object %s already exists with different content", sha)
	}

	return sha, nil
}

type Node struct {
	Mode FileMode
	Name string
	Sha1 string
}

func (r *Repository) LsTree(TreeSHA string) ([]Node, error) {
	file, err := r.Storage.ObjectReader(TreeSHA)
	if err != nil {
		return []Node{}, err
	}
	defer file.Close()

	reader, err := zlib.NewReader(file)
	if err != nil {
		return []Node{}, err
	}

	content, err := io.ReadAll(reader)
	if err != nil {
		return []Node{}, err
	}

	treeHeader := []byte("tree ")
	if !bytes.HasPrefix(content, treeHeader) {
		return []Node{}, errors.New("not correct tree header: " + string(content[:len(treeHeader)]))
	}

	delimIndex := bytes.Index(content, []byte{0})

	size, err := strconv.Atoi(string(content[len(treeHeader):delimIndex]))
	if err != nil {
		return []Node{}, errors.New("not correct tree header: " + string(content[:delimIndex]))
	}
	content = content[delimIndex+1:]

	if size != len(content) {
		return []Node{}, fmt.Errorf("not correct tree size in header. Expected: %d. Actual: %d", len(content), size)
	}

	var nodes []Node

	for len(content) > 0 {
		modeNameDelim := bytes.Index(content, []byte(" "))
		if modeNameDelim == -1 {
			return []Node{}, fmt.Errorf("not correct tree object: not found delimeter between mode and name")
		}
		mode := string(content[:modeNameDelim])
		content = content[modeNameDelim+1:]

		nameShaDelim := bytes.Index(content, []byte{0})
		if nameShaDelim == -1 {
			return []Node{}, fmt.Errorf("not correct tree object: not found delimeter between name and sha")
		}
		name := string(content[:nameShaDelim])
		sha := hex.EncodeToString(content[nameShaDelim+1 : nameShaDelim+1+20])

		content = content[nameShaDelim+1+20:]

		fm, err := New(mode)
		if err != nil {
			return []Node{}, fmt.Errorf("not correct file mood in tree object: %s:%s", mode, name)
		}

		nodes = append(nodes, Node{
			Mode: fm,
			Name: name,
			Sha1: sha,
		})

	}

	return nodes, nil
}

func (r *Repository) CreateTree(dirPath string) ([]Node, error) {
	files, err := os.ReadDir(dirPath)
	if err != nil {
		return []Node{}, err
	}

	//We are going to ignore files that are listed in .gitignore and the .git directory itself
	gitignore, err := ReadGitignore(dirPath)
	if err != nil {
		return []Node{}, err
	}

	var nodes []Node

	for _, file := range files {

		if file.IsDir() {
			if checkIgnore("/"+file.Name(), gitignore) {
				//fmt.Println("Ignoring directory: " + "/"+file.Name())
				continue
			}
			sha, err := r.WriteTree(dirPath + "/" + file.Name())
			if err != nil {
				return []Node{}, err
			}
			nodes = append(nodes, Node{
				Mode: Dir,
				Name: file.Name(),
				Sha1: sha,
			})
		} else {
			if checkIgnore(file.Name(), gitignore) {
				continue
			}
			content, err := os.ReadFile(dirPath + "/" + file.Name())
			if err != nil {
				return []Node{}, err
			}
			sha, err := r.HashObject(content)
			if err != nil {
				return []Node{}, err
			}
			mode, err := GetMode(dirPath + "/" + file.Name())
			if err != nil {
				return []Node{}, err
			}
			nodes = append(nodes, Node{
				Mode: mode,
				Name: file.Name(),
				Sha1: sha,
			})
		}
	}

	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].Name < nodes[j].Name
	})

	return nodes, nil

}

func (r *Repository) WriteTree(dirPath string) (treeSHA string, err error) {
	nodes, err := r.CreateTree(dirPath)
	if err != nil {
		return "", err
	}

	var treeContent bytes.Buffer

	for _, node := range nodes {
		treeContent.WriteString(strings.TrimLeft(node.Mode.String(), "0"))
		treeContent.WriteByte(' ')
		treeContent.WriteString(node.Name)
		treeContent.WriteByte(0)

		shaBytes, err := hex.DecodeString(node.Sha1)
		if err != nil {
			return "", err
		}
		treeContent.Write(shaBytes)
	}

	treeSHA, err = r.WriteObject(treeContent.Bytes(), "tree")
	if err != nil {
		return treeSHA, errors.New("can`t write tree object: " + err.Error())
	}

	return treeSHA, nil
}

type Commit struct {
	SHA1      string
	TreeSHA   string
	ParentSHA string
	Message   string
	Author    Author
	Committer Committer
}

type Author struct {
	Name      string
	Email     string
	Timestamp int64
	Timezone  int
}

type Committer struct {
	Name      string
	Email     string
	Timestamp int64
	Timezone  int
}

func (r *Repository) CommitTree(c Commit) (sha string, err error) {
	var body []byte
	body = append(body, fmt.Appendf(nil, "tree %s\n", c.TreeSHA)...)
	if c.ParentSHA != "" {
		body = append(body, fmt.Appendf(nil, "parent %s\n", c.ParentSHA)...)
	}
	body = append(body, fmt.Appendf(nil, "author %s <%s> %d %s\n", c.Author.Name, c.Author.Email, c.Author.Timestamp, utils.FormatTimezone(c.Author.Timezone))...)
	body = append(body, fmt.Appendf(nil, "committer %s <%s> %d %s\n", c.Committer.Name, c.Committer.Email, c.Committer.Timestamp, utils.FormatTimezone(c.Committer.Timezone))...)
	body = append(body, fmt.Appendf(nil, "\n")...)
	body = append(body, []byte(c.Message+"\n")...)

	sha, err = r.WriteObject(body, "commit")
	if err != nil {
		return sha, errors.New("can`t write commit object: " + err.Error())
	}

	return sha, nil
}
