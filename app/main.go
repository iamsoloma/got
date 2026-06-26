package main

import (
	"compress/zlib"
	"crypto/sha1"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func main() {

	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: mygit <command> [<args>...]\n")
		os.Exit(1)
	}

	switch command := os.Args[1]; command {
	case "init":

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

	case "cat-file":
		sha := os.Args[3]

		path := fmt.Sprintf(".git/objects/%s/%s", sha[:2], sha[2:])

		file, err := os.Open(path)
		if err != nil {
			fmt.Fprintf(os.Stderr,"%s", err.Error())
		}

		r, err := zlib.NewReader(file)
		if err != nil {
			fmt.Fprintf(os.Stderr,"%s", err.Error())
		}

		s, err := io.ReadAll(r)
		if err != nil {
			fmt.Fprintf(os.Stderr,"%s", err.Error())
		}

		parts := strings.Split(string(s), "\x00")

		fmt.Print(parts[1])

		r.Close()

	case "hash-object":
		content, err := os.ReadFile(os.Args[3])
		if err != nil {
			fmt.Fprintf(os.Stderr,"%s", err.Error())
		}

		object := fmt.Sprintf("blob %d\x00%s", len(content), content)
		sha := fmt.Sprintf("%x", sha1.Sum([]byte(object)))

		path := fmt.Sprintf(".git/objects/%s/%s", sha[:2], sha[2:])
		err = os.MkdirAll(filepath.Dir(path), os.ModePerm)
		if err != nil {
			fmt.Fprintf(os.Stderr,"%s", err.Error())
		}

		file, err := os.Create(path)
		if err != nil {
			fmt.Fprintf(os.Stderr,"%s", err.Error())
		}
		defer file.Close()

		writer := zlib.NewWriter(file)
		defer writer.Close()
		_, err = writer.Write([]byte(object))
		if err != nil {
			fmt.Fprintf(os.Stderr,"%s", err.Error())
		}

		fmt.Println(sha)

	default:
		fmt.Fprintf(os.Stderr, "Unknown command %s\n", command)
		os.Exit(1)
	}
}
