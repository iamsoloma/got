package main

import (
	"fmt"
	"got/git"
	"os"
	"time"
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

	case "ls-tree":
		sha := os.Args[2]

		nodes, err := git.LsTree(sha)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s", err.Error())
		}
		for _, node := range nodes {
			nodeType := "blob"
			if node.Mode == git.Dir {
				nodeType = "tree"
			}
			fmt.Printf("%s %s %s %s\n", node.Mode, nodeType, node.Sha1, node.Name)
		}
	case "write-tree":
		treeSHA, err := git.WriteTree(".")
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s", err.Error())
		}
		fmt.Println(treeSHA)

	case "commit-tree":
		treeSHA := os.Args[2]
		parentSHA := os.Args[4]
		message := os.Args[6]

		_, tz := time.Now().Zone()
		author := git.Author{
			Name:      "soloma",
			Email:     "EgorSolomahin1@yandex.ru",
			Timestamp: time.Now().UTC().Unix(),
			Timezone:  tz,
		}
		committer := git.Committer{
			Name:      author.Name,
			Email:     author.Email,
			Timestamp: author.Timestamp,
			Timezone:  author.Timezone,
		}

		commit := git.Commit{
			TreeSHA:   treeSHA,
			ParentSHA: parentSHA,
			Message:   message,
			Author:    author,
			Committer: committer,
		}

		sha, err := git.CommitTree(commit)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s", err.Error())
		}
		fmt.Println(sha)

	default:
		fmt.Fprintf(os.Stderr, "Unknown command %s\n", command)
		os.Exit(1)
	}
}
