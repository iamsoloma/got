package main

import (
	"fmt"
	"got/git"
	"os"
)

func main() {

	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: mygit <command> [<args>...]\n")
		os.Exit(1)
	}

	switch command := os.Args[1]; command {
	case "init":
		git.Init()

	case "cat-file":
		sha := os.Args[3]

		out := git.CatFile(sha)

		fmt.Print(out)

	case "hash-object":
		hash := git.HashObject(os.Args[3])

		fmt.Println(hash)

	default:
		fmt.Fprintf(os.Stderr, "Unknown command %s\n", command)
		os.Exit(1)
	}
}
