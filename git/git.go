package git

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func Init() {

	for _, dir := range []string{".git", ".git/objects", ".git/refs"} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			fmt.Fprintf(os.Stderr, "Error creating directory: %s\n", err)
		}
	}

	headFileContents := []byte("ref: refs/heads/main\n")
	if err := os.WriteFile(".git/HEAD", headFileContents, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing file: %s\n", err)
	}

	fmt.Println("Initialized git directory")
}

func CatFile(objectSha string) string {

	path := fmt.Sprintf(".git/objects/%s/%s", objectSha[:2], objectSha[2:])

	file, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s", err.Error())
	}

	r, err := zlib.NewReader(file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s", err.Error())
	}

	s, err := io.ReadAll(r)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s", err.Error())
	}

	parts := strings.Split(string(s), "\x00")

	r.Close()

	return parts[1]
}

func HashObject(filename string) string {
	content, err := os.ReadFile(filename)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s", err.Error())
	}

	object := fmt.Sprintf("blob %d\x00%s", len(content), content)
	sha := fmt.Sprintf("%x", sha1.Sum([]byte(object)))

	path := fmt.Sprintf(".git/objects/%s/%s", sha[:2], sha[2:])
	err = os.MkdirAll(filepath.Dir(path), os.ModePerm)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s", err.Error())
	}

	file, err := os.Create(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s", err.Error())
	}
	defer file.Close()

	writer := zlib.NewWriter(file)
	defer writer.Close()
	_, err = writer.Write([]byte(object))
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s", err.Error())
	}

	return sha
}

type Node struct {
	Mode FileMode
	Name string
	Sha1 string
}

func LsTree(TreeSHA string) ([]Node, error) {
	path := fmt.Sprintf(".git/objects/%s/%s", TreeSHA[:2], TreeSHA[2:])
	file, err := os.Open(path)
	if err != nil {
		return []Node{}, err
	}
	defer file.Close()

	r, err := zlib.NewReader(file)
	if err != nil {
		return []Node{}, err
	}

	content, err := io.ReadAll(r)
	if err != nil {
		return []Node{}, err
	}
	r.Close()

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
