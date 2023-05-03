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
	"sync"
)

var CLI struct {
	DirA       string `arg:"" help:"Directory A." type:"existingdir"`
	DirB       string `arg:"" help:"Directory B." type:"existingdir"`
	UseModTime bool   `default:"true" help:"Use file mod time (default true)."`
	UseSize    bool   `default:"true" help:"Use file size (default true)."`
	UseMode    bool   `default:"false" help:"Use file mode (default false)."`
	UseName    bool   `default:"false" help:"Use file name even when there is no collision (default false)."`
	Workers    int    `default:"12" help:"Count of parallel workers for scanning."`
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
	root      *dirtree.Dirent
	basePath  string
	hashMap   map[uint64][]*dirtree.Dirent
	entryMap  map[*dirtree.Dirent]uint64
	mapMutex  sync.Mutex
	treeMutex sync.Mutex
	wg        sync.WaitGroup
}

type ScanEntry struct {
	path  string
	isDir bool
}

func mapDir(dir string) (*DirMap, error) {
	dirMap := DirMap{
		root:     dirtree.New(""),
		basePath: dir,
		hashMap:  map[uint64][]*dirtree.Dirent{},
		entryMap: map[*dirtree.Dirent]uint64{},
	}
	dirChan := make(chan ScanEntry, 1024)
	for i := 0; i < CLI.Workers/2; i++ {
		go func() {
			for entry := range dirChan {
				if err := mapWorker(entry, &dirMap, dirChan); err != nil {
					log.Fatalln(err)
				}
			}
		}()
	}
	dirMap.wg.Add(1)
	dirChan <- ScanEntry{"", true}
	dirMap.wg.Wait()
	return &dirMap, nil
}

func mapWorker(scanEntry ScanEntry, dirMap *DirMap, scanChan chan ScanEntry) error {
	defer dirMap.wg.Done()
	if scanEntry.isDir {
		children, err := os.ReadDir(filepath.Join(dirMap.basePath, scanEntry.path))
		if err != nil {
			return err
		}
		dirMap.wg.Add(len(children))
		for _, child := range children {
			newEntry := ScanEntry{filepath.Join(scanEntry.path, child.Name()), child.IsDir()}
			select {
			case scanChan <- newEntry:
			default:
				if err := mapWorker(newEntry, dirMap, scanChan); err != nil {
					return err
				}
			}
		}
		return nil
	}
	curNode := dirMap.root
	dirMap.treeMutex.Lock()
	for _, part := range strings.Split(scanEntry.path, string(os.PathSeparator)) {
		newNode := curNode.Child(part)
		var err error
		if newNode == nil {
			newNode, err = curNode.Add(part)
			if err != nil {
				return err
			}
		}
		curNode = newNode
	}
	dirMap.treeMutex.Unlock()
	info, err := os.Lstat(filepath.Join(dirMap.basePath, scanEntry.path))
	if err != nil {
		return err
	}
	h := hash(info)
	dirMap.mapMutex.Lock()
	if v, ok := dirMap.hashMap[h]; ok {
		dirMap.hashMap[h] = append(v, curNode)
	} else {
		dirMap.hashMap[h] = []*dirtree.Dirent{curNode}
	}
	dirMap.entryMap[curNode] = h
	dirMap.mapMutex.Unlock()
	return nil
}

func walkDir(mapA, mapB *DirMap, dirA *dirtree.Dirent) {
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
	log.Println("Mapping directories...")
	var wg sync.WaitGroup
	wg.Add(2)
	var dirA, dirB *DirMap
	go func() {
		result, err := mapDir(CLI.DirA)
		if err != nil {
			log.Fatalln(err)
		}
		dirA = result
		log.Println("Finished " + CLI.DirA)
		wg.Done()
	}()
	go func() {
		result, err := mapDir(CLI.DirB)
		if err != nil {
			log.Fatalln(err)
		}
		dirB = result
		log.Println("Finished " + CLI.DirB)
		wg.Done()
	}()
	wg.Wait()
	log.Println("Comparing...")
	fmt.Printf("========== Only in %s ==========\n", CLI.DirA)
	walkDir(dirA, dirB, dirA.root)
	fmt.Printf("========== Only in %s ==========\n", CLI.DirB)
	walkDir(dirB, dirA, dirB.root)
	log.Println("Done")
	return nil
}
