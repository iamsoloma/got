package git

import (
	"os"
	"testing"
)

func TestFileMode_New(t *testing.T) {
	tests := []struct {
		input    string
		expected FileMode
	}{
		{"100644", Regular},
		{"100755", Executable},
		{"120000", SymLink},
		{"040000", Dir},
		{"160000", Submodule},
		{"0", Empty},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			fm, err := New(tt.input)
			if err != nil {
				t.Fatal(err)
			}
			if fm != tt.expected {
				t.Errorf("New(%q) = %d, want %d", tt.input, fm, tt.expected)
			}
		})
	}
}

func TestFileMode_String(t *testing.T) {
	tests := []struct {
		mode     FileMode
		expected string
	}{
		{Regular, "0100644"},
		{Executable, "0100755"},
		{SymLink, "0120000"},
		{Dir, "0040000"},
		{Submodule, "0160000"},
		{Empty, "0000000"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if got := tt.mode.String(); got != tt.expected {
				t.Errorf("FileMode.String() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestGetMode(t *testing.T) {
	dir := t.TempDir()
	origDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(origDir)

	// Regular file
	createFile(t, "regular.txt", "hello")
	mode, err := GetMode("regular.txt")
	if err != nil {
		t.Fatal(err)
	}
	if mode != Regular {
		t.Errorf("GetMode(regular.txt) = %d, want %d", mode, Regular)
	}

	// Executable file
	createFile(t, "exec.sh", "#!/bin/sh\n")
	os.Chmod("exec.sh", 0755)
	mode, err = GetMode("exec.sh")
	if err != nil {
		t.Fatal(err)
	}
	if mode != Executable {
		t.Errorf("GetMode(exec.sh) = %d, want %d", mode, Executable)
	}

	// Directory
	os.MkdirAll("mydir", 0755)
	mode, err = GetMode("mydir")
	if err != nil {
		t.Fatal(err)
	}
	if mode != Dir {
		t.Errorf("GetMode(mydir) = %d, want %d", mode, Dir)
	}
}