package git

import (
	"os"
	"slices"
	"strings"
)

func checkIgnore(filePath string, gitignore []string) bool {
	if slices.Contains(gitignore, filePath) {
		return true
	}
	return false
}

func ReadGitignore(dirPath string) ([]string, error) {
	var err error
	gitignore := []string{}
	gitignore = append(gitignore, "/.git")
	gitignoreFile := ".gitignore"
	_, existerr := os.Stat(dirPath + "/" + gitignoreFile)
	var ignoreContent []byte
	if !os.IsNotExist(existerr) {
		ignoreContent, err = os.ReadFile(dirPath + "/" + gitignoreFile)
		if err != nil {
			return []string{}, err
		}
	}
	for _, line := range strings.Split(string(ignoreContent), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			gitignore = append(gitignore, line)
		}
	}

	//fmt.Println("Gitignore patterns: ", gitignore)

	return gitignore, nil
}
