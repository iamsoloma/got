# got — a custom Git implementation in Go

## Overview

`got` is a CLI tool that implements core Git internals from scratch in Go. It supports: `init`, `cat-file`, `hash-object`, `ls-tree`, `write-tree`, and `commit-tree`.

## Project structure

```
app/main.go       — CLI entry point (command dispatch)
git/
  storage.go      — Storage interface (abstracts object/ref storage)
  fs_storage.go   — FSStorage: filesystem implementation of Storage
  object.go       — Object store functions (Init, HashObject, WriteObject, LsTree, WriteTree, CommitTree, CatFile)
  refs.go         — Ref functions (ReadHead, UpdateHead, ReadReference, UpdateReference, ListLocalBranches, CreateTag, ReadTag, ReadAnnotatedTag)
  filemode.go     — FileMode type and helpers
  gitignore.go    — .gitignore parsing
  git_test.go     — Full test suite (do not modify)
utils/
  timezone.go     — Timezone formatting helpers
```

## Running tests

```bash
GOTOOLCHAIN=local go test ./git/... -v
```

## Building

```bash
GOTOOLCHAIN=local go build -o got ./app
```

## Architecture note

All package-level git functions delegate to `DefaultStorage` (a `Storage` interface), which defaults to `FSStorage` — a plain filesystem backend using `.git/`. To use a different backend (e.g. in-memory for testing), replace `git.DefaultStorage` before calling any functions.

## Go version

The installed toolchain is Go 1.21. `go.mod` is pinned to `go 1.21`. Always run with `GOTOOLCHAIN=local` to avoid toolchain auto-download.

## User preferences

- Tests in `git/git_test.go` must not be modified.
