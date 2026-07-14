package main

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gammazero/dirtree"
)

func writeComparableFile(t *testing.T, dir, name, contents string, modTime time.Time) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
}

func leafPaths(root *dirtree.Dirent) []string {
	var paths []string
	var walk func(*dirtree.Dirent)
	walk = func(entry *dirtree.Dirent) {
		children := entry.List()
		if len(children) == 0 {
			if entry.Path() != "" {
				paths = append(paths, strings.TrimPrefix(entry.Path(), "/"))
			}
			return
		}
		for _, name := range children {
			walk(entry.Child(name))
		}
	}
	walk(root)
	sort.Strings(paths)
	return paths
}

func TestCompareDirsPreservesDuplicateCardinality(t *testing.T) {
	oldCLI := CLI
	defer func() { CLI = oldCLI }()
	CLI.Workers = 2
	CLI.UseModTime = true
	CLI.UseSize = true

	dirA := t.TempDir()
	dirB := t.TempDir()
	modTime := time.Unix(1_700_000_000, 123_456_789)
	writeComparableFile(t, dirA, "x", "a", modTime)
	writeComparableFile(t, dirA, "y", "b", modTime)
	writeComparableFile(t, dirB, "x", "c", modTime)

	mapA, err := mapDir(dirA)
	if err != nil {
		t.Fatal(err)
	}
	mapB, err := mapDir(dirB)
	if err != nil {
		t.Fatal(err)
	}
	diffA, diffB, err := compareDirs(mapA, mapB)
	if err != nil {
		t.Fatal(err)
	}

	if got, want := leafPaths(diffA), []string{"y"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("only-in-A paths = %v, want %v", got, want)
	}
	if got := leafPaths(diffB); len(got) != 0 {
		t.Fatalf("only-in-B paths = %v, want none", got)
	}
}

func TestCompareDirsPairsRenamedDuplicatesOneToOne(t *testing.T) {
	oldCLI := CLI
	defer func() { CLI = oldCLI }()
	CLI.Workers = 2
	CLI.UseModTime = true
	CLI.UseSize = true

	dirA := t.TempDir()
	dirB := t.TempDir()
	modTime := time.Unix(1_700_000_000, 123_456_789)
	writeComparableFile(t, dirA, "x", "a", modTime)
	writeComparableFile(t, dirA, "y", "b", modTime)
	writeComparableFile(t, dirB, "u", "c", modTime)
	writeComparableFile(t, dirB, "v", "d", modTime)

	mapA, err := mapDir(dirA)
	if err != nil {
		t.Fatal(err)
	}
	mapB, err := mapDir(dirB)
	if err != nil {
		t.Fatal(err)
	}
	diffA, diffB, err := compareDirs(mapA, mapB)
	if err != nil {
		t.Fatal(err)
	}

	if got := leafPaths(diffA); len(got) != 0 {
		t.Fatalf("only-in-A paths = %v, want none", got)
	}
	if got := leafPaths(diffB); len(got) != 0 {
		t.Fatalf("only-in-B paths = %v, want none", got)
	}
}

func TestZeroWorkersReturnsError(t *testing.T) {
	oldCLI := CLI
	defer func() { CLI = oldCLI }()
	CLI.Workers = 0

	_, err := mapDir(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "at least 1") {
		t.Fatalf("mapDir error = %v, want worker validation error", err)
	}
	if err := work(); err == nil || !strings.Contains(err.Error(), "at least 1") {
		t.Fatalf("work error = %v, want worker validation error", err)
	}
}
