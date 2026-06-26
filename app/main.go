package main

import (
	"compress/zlib"
	"fmt"
	"io"
	"os"
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
			fmt.Errorf("%s", err.Error())
		}

		r, err := zlib.NewReader(file)
		if err != nil {
			fmt.Errorf("%s", err.Error())
		}

		s, err := io.ReadAll(r)
		if err != nil {
			fmt.Errorf("%s", err.Error())
		}

		parts := strings.Split(string(s), "\x00")

		fmt.Print(parts[1])

		r.Close()

	default:
		fmt.Fprintf(os.Stderr, "Unknown command %s\n", command)
		os.Exit(1)
	}
}
