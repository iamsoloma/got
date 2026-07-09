package git

import (
	"fmt"
	"io/fs"
	"os"
	"strconv"
)

type FileMode uint

const (
	Empty FileMode = 0
	Dir   FileMode = 040000

	Regular    FileMode = 0100644
	Executable FileMode = 0100755
	SymLink    FileMode = 0120000
	Submodule  FileMode = 0160000
)

func New(str string) (FileMode, error) {
	fm, err := strconv.ParseInt(str, 8, 32)
	if err != nil {
		return Empty, err
	}
	return FileMode(fm), nil
}

func (m FileMode) String() string {
	return fmt.Sprintf("%07o", uint32(m))
}

func GetMode(filePath string) (FileMode, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		return Empty, err
	}

	if info.Mode()&fs.ModeDir != 0 {
		return Dir, nil
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return SymLink, nil
	}
	if isExec := 0o111; info.Mode().Perm()&fs.FileMode(isExec) != 0 {
		return Executable, nil
	}
	return Regular, nil
}
