package main

import (
	"fmt"
	"got/git"
	"got/storage/filesystem"
	"os"
	"time"
)

func main() {

	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: mygit <command> [<args>...]\n")
		os.Exit(1)
	}

	storage := &filesystem.FileSystemStorage{}
	repo := &git.Repository{Storage: &filesystem.FileSystemStorage{}}
	switch command := os.Args[1]; command {
	case "init":
		git.Init(storage, "./", "main")

	case "cat-file":
		sha := os.Args[3]

		out := repo.CatFile(sha)

		fmt.Print(out)

	case "hash-object":
		content, err := os.ReadFile(os.Args[3])
		if err != nil {
			fmt.Fprintf(os.Stderr, "can`t read a file: %s", err.Error())
		}
		hash, err := repo.HashObject(content)

		if err != nil {
			fmt.Fprintf(os.Stderr, "hash object error: %s", err.Error())
		}
		fmt.Println(hash)

	case "ls-tree":
		sha := os.Args[2]

		nodes, err := repo.LsTree(sha)
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
		treeSHA, err := repo.WriteTree(".")
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

		sha, err := repo.CommitTree(commit)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s", err.Error())
		}
		fmt.Println(sha)

	default:
		fmt.Fprintf(os.Stderr, "Unknown command %s\n", command)
		os.Exit(1)
	}
}
