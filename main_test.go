package main

import (
	"fmt"
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
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
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

func configureTestCLI(t *testing.T) {
	t.Helper()
	oldCLI := CLI
	t.Cleanup(func() { CLI = oldCLI })
	CLI.UseMode = false
	CLI.Workers = 2
	CLI.UseModTime = true
	CLI.UseSize = true
	CLI.UseName = false
	CLI.UsePath = false
}

func compareTestDirs(t *testing.T, dirA, dirB string) (*dirtree.Dirent, *dirtree.Dirent) {
	t.Helper()
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
	return diffA, diffB
}

func TestCompareDirsPreservesDuplicateCardinality(t *testing.T) {
	configureTestCLI(t)

	dirA := t.TempDir()
	dirB := t.TempDir()
	modTime := time.Unix(1_700_000_000, 123_456_789)
	writeComparableFile(t, dirA, "x", "a", modTime)
	writeComparableFile(t, dirA, "y", "b", modTime)
	writeComparableFile(t, dirB, "x", "c", modTime)

	diffA, diffB := compareTestDirs(t, dirA, dirB)

	if got, want := leafPaths(diffA), []string{"y"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("only-in-A paths = %v, want %v", got, want)
	}
	if got := leafPaths(diffB); len(got) != 0 {
		t.Fatalf("only-in-B paths = %v, want none", got)
	}
}

func TestCompareDirsPairsRenamedDuplicatesOneToOne(t *testing.T) {
	configureTestCLI(t)

	dirA := t.TempDir()
	dirB := t.TempDir()
	modTime := time.Unix(1_700_000_000, 123_456_789)
	writeComparableFile(t, dirA, "x", "a", modTime)
	writeComparableFile(t, dirA, "y", "b", modTime)
	writeComparableFile(t, dirB, "u", "c", modTime)
	writeComparableFile(t, dirB, "v", "d", modTime)

	diffA, diffB := compareTestDirs(t, dirA, dirB)

	if got := leafPaths(diffA); len(got) != 0 {
		t.Fatalf("only-in-A paths = %v, want none", got)
	}
	if got := leafPaths(diffB); len(got) != 0 {
		t.Fatalf("only-in-B paths = %v, want none", got)
	}
}

func TestCompareDirsReportsUnbalancedRenamedDuplicates(t *testing.T) {
	configureTestCLI(t)

	dirA := t.TempDir()
	dirB := t.TempDir()
	modTime := time.Unix(1_700_000_000, 123_456_789)
	writeComparableFile(t, dirA, "a", "a", modTime)
	writeComparableFile(t, dirA, "b", "b", modTime)
	writeComparableFile(t, dirA, "c", "c", modTime)
	writeComparableFile(t, dirB, "renamed", "d", modTime)

	diffA, diffB := compareTestDirs(t, dirA, dirB)
	if got, want := leafPaths(diffA), []string{"b", "c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("only-in-A paths = %v, want %v", got, want)
	}
	if got := leafPaths(diffB); len(got) != 0 {
		t.Fatalf("only-in-B paths = %v, want none", got)
	}
}

func TestCompareDirsCoreBehavior(t *testing.T) {
	tests := []struct {
		name   string
		filesA map[string]string
		filesB map[string]string
		wantA  []string
		wantB  []string
	}{
		{
			name:   "identical attributes at same path",
			filesA: map[string]string{"x": "a"},
			filesB: map[string]string{"x": "b"},
		},
		{
			name:   "unique renamed file",
			filesA: map[string]string{"old-name": "a"},
			filesB: map[string]string{"new-name": "b"},
		},
		{
			name:   "changed size at same path",
			filesA: map[string]string{"x": "a"},
			filesB: map[string]string{"x": "longer"},
			wantA:  []string{"x"},
			wantB:  []string{"x"},
		},
		{
			name:   "extra duplicate in B preserves equal path",
			filesA: map[string]string{"x": "a"},
			filesB: map[string]string{"x": "b", "y": "c"},
			wantB:  []string{"y"},
		},
		{
			name: "nested unmatched files retain their paths",
			filesA: map[string]string{
				"nested/a": "a",
				"nested/b": "longer",
			},
			filesB: map[string]string{},
			wantA:  []string{"nested/a", "nested/b"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configureTestCLI(t)
			dirA := t.TempDir()
			dirB := t.TempDir()
			modTime := time.Unix(1_700_000_000, 123_456_789)
			for name, contents := range test.filesA {
				writeComparableFile(t, dirA, name, contents, modTime)
			}
			for name, contents := range test.filesB {
				writeComparableFile(t, dirB, name, contents, modTime)
			}

			diffA, diffB := compareTestDirs(t, dirA, dirB)
			if got := leafPaths(diffA); !reflect.DeepEqual(got, test.wantA) {
				t.Fatalf("only-in-A paths = %v, want %v", got, test.wantA)
			}
			if got := leafPaths(diffB); !reflect.DeepEqual(got, test.wantB) {
				t.Fatalf("only-in-B paths = %v, want %v", got, test.wantB)
			}
		})
	}
}

func TestModificationTimeDifferenceIsReported(t *testing.T) {
	configureTestCLI(t)

	dirA := t.TempDir()
	dirB := t.TempDir()
	writeComparableFile(t, dirA, "x", "a", time.Unix(1_700_000_000, 0))
	writeComparableFile(t, dirB, "x", "b", time.Unix(1_800_000_000, 0))

	diffA, diffB := compareTestDirs(t, dirA, dirB)
	if got, want := leafPaths(diffA), []string{"x"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("only-in-A paths = %v, want %v", got, want)
	}
	if got, want := leafPaths(diffB), []string{"x"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("only-in-B paths = %v, want %v", got, want)
	}
}

func TestComparisonOptions(t *testing.T) {
	t.Run("use name prevents rename matching", func(t *testing.T) {
		configureTestCLI(t)
		CLI.UseName = true
		dirA := t.TempDir()
		dirB := t.TempDir()
		modTime := time.Unix(1_700_000_000, 123_456_789)
		writeComparableFile(t, dirA, "old-name", "a", modTime)
		writeComparableFile(t, dirB, "new-name", "b", modTime)

		diffA, diffB := compareTestDirs(t, dirA, dirB)
		if got, want := leafPaths(diffA), []string{"old-name"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("only-in-A paths = %v, want %v", got, want)
		}
		if got, want := leafPaths(diffB), []string{"new-name"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("only-in-B paths = %v, want %v", got, want)
		}
	})

	t.Run("use path prevents move matching", func(t *testing.T) {
		configureTestCLI(t)
		CLI.UsePath = true
		dirA := t.TempDir()
		dirB := t.TempDir()
		modTime := time.Unix(1_700_000_000, 123_456_789)
		writeComparableFile(t, dirA, "old/x", "a", modTime)
		writeComparableFile(t, dirB, "new/x", "b", modTime)

		diffA, diffB := compareTestDirs(t, dirA, dirB)
		if got, want := leafPaths(diffA), []string{"old/x"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("only-in-A paths = %v, want %v", got, want)
		}
		if got, want := leafPaths(diffB), []string{"new/x"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("only-in-B paths = %v, want %v", got, want)
		}
	})

	t.Run("disabled attributes are ignored", func(t *testing.T) {
		configureTestCLI(t)
		CLI.UseModTime = false
		CLI.UseSize = false
		dirA := t.TempDir()
		dirB := t.TempDir()
		writeComparableFile(t, dirA, "x", "short", time.Unix(1_700_000_000, 0))
		writeComparableFile(t, dirB, "x", "much longer", time.Unix(1_800_000_000, 0))

		diffA, diffB := compareTestDirs(t, dirA, dirB)
		if got := leafPaths(diffA); len(got) != 0 {
			t.Fatalf("only-in-A paths = %v, want none", got)
		}
		if got := leafPaths(diffB); len(got) != 0 {
			t.Fatalf("only-in-B paths = %v, want none", got)
		}
	})
}

func TestMapDirHandlesMoreEntriesThanQueueCapacity(t *testing.T) {
	configureTestCLI(t)
	CLI.Workers = 1
	dir := t.TempDir()
	const fileCount = 1025
	for i := 0; i < fileCount; i++ {
		path := filepath.Join(dir, fmt.Sprintf("file-%04d", i))
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	dirMap, err := mapDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(dirMap.entryMap); got != fileCount {
		t.Fatalf("mapped file count = %d, want %d", got, fileCount)
	}
}

func TestZeroWorkersReturnsError(t *testing.T) {
	for _, workers := range []int{0, -1} {
		t.Run(fmt.Sprintf("workers=%d", workers), func(t *testing.T) {
			configureTestCLI(t)
			CLI.Workers = workers

			_, err := mapDir(t.TempDir())
			if err == nil || !strings.Contains(err.Error(), "at least 1") {
				t.Fatalf("mapDir error = %v, want worker validation error", err)
			}
			if err := work(); err == nil || !strings.Contains(err.Error(), "at least 1") {
				t.Fatalf("work error = %v, want worker validation error", err)
			}
		})
	}
}
