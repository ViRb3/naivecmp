package main

import (
	"encoding/binary"
	"fmt"
	"github.com/alecthomas/kong"
	"github.com/gammazero/dirtree"
	"hash/maphash"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
)

var CLI struct {
	DirA       string `arg:"" help:"Directory A." type:"existingdir"`
	DirB       string `arg:"" help:"Directory B." type:"existingdir"`
	UseModTime bool   `default:"true" help:"Use file mod time (default true)."`
	UseSize    bool   `default:"true" help:"Use file size (default true)."`
	UseMode    bool   `default:"false" help:"Use file mode (default false)."`
	UseName    bool   `default:"false" help:"Use file name even when there is no collision (default false)."`
}

func main() {
	kong.Parse(&CLI,
		kong.Name("naivecmp"),
		kong.Description("Compare directories by fuzzy-matching file attributes without checking contents."),
		kong.UsageOnError(),
		kong.ConfigureHelp(kong.HelpOptions{
			Compact: true,
		}),
	)
	if err := work(); err != nil {
		log.Fatalln(err)
	}
}

var seed = maphash.MakeSeed()

func hash(info fs.FileInfo) uint64 {
	data := make([]byte, 0, 32)
	if CLI.UseMode {
		data = binary.LittleEndian.AppendUint32(data, uint32(info.Mode()))
	}
	if CLI.UseModTime {
		data = binary.LittleEndian.AppendUint64(data, uint64(info.ModTime().UnixNano()))
	}
	if CLI.UseSize {
		data = binary.LittleEndian.AppendUint64(data, uint64(info.Size()))
	}
	if CLI.UseName {
		data = append(data, []byte(info.Name())...)
	}
	return maphash.Bytes(seed, data)
}

type DirMap struct {
	root     *dirtree.Dirent
	hashMap  map[uint64][]*dirtree.Dirent
	entryMap map[*dirtree.Dirent]uint64
}

func mapDir(dir string) (DirMap, error) {
	dirMap := DirMap{
		root:     dirtree.New(""),
		hashMap:  map[uint64][]*dirtree.Dirent{},
		entryMap: map[*dirtree.Dirent]uint64{},
	}
	if err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		curNode := dirMap.root
		for _, part := range strings.Split(rel, string(os.PathSeparator)) {
			newNode := curNode.Child(part)
			if newNode == nil {
				newNode, err = curNode.Add(part)
				if err != nil {
					return err
				}
			}
			curNode = newNode
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		h := hash(info)
		if v, ok := dirMap.hashMap[h]; ok {
			dirMap.hashMap[h] = append(v, curNode)
		} else {
			dirMap.hashMap[h] = []*dirtree.Dirent{curNode}
		}
		dirMap.entryMap[curNode] = h
		return nil
	}); err != nil {
		return DirMap{}, err
	}
	return dirMap, nil
}

func walkDir(mapA, mapB DirMap, dirA *dirtree.Dirent) {
	if len(dirA.Children()) > 0 {
		dirA.ForChild(func(d *dirtree.Dirent) bool {
			walkDir(mapA, mapB, d)
			return true
		})
		return
	}
	h, ok := mapA.entryMap[dirA]
	if !ok {
		// this is a directory
		return
	}
	var matched bool
	if matches, ok := mapB.hashMap[h]; !ok {
		// file is missing from dirB
		matched = false
	} else if len(matches) == 1 {
		// file is present in dirB
		matched = true
	} else {
		// if multiple files in dirB have the same hash, fall back to comparing full path
		matched = false
		for _, match := range matches {
			if match.Path() == dirA.Path() {
				matched = true
				break
			}
		}
	}
	if !matched {
		fmt.Println(dirA.Path())
	}
}

func work() error {
	log.Println("Mapping directory A...")
	dirA, err := mapDir(CLI.DirA)
	if err != nil {
		return err
	}
	log.Println("Mapping directory B...")
	dirB, err := mapDir(CLI.DirB)
	if err != nil {
		return err
	}
	log.Println("Comparing...")
	fmt.Printf("========== Only in %s ==========\n", CLI.DirA)
	walkDir(dirA, dirB, dirA.root)
	fmt.Printf("========== Only in %s ==========\n", CLI.DirB)
	walkDir(dirB, dirA, dirB.root)
	log.Println("Done")
	return nil
}
