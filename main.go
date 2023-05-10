package main

import (
	"encoding/binary"
	"fmt"
	"github.com/alecthomas/kong"
	"github.com/gammazero/dirtree"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"hash/maphash"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strconv"
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
	Workers    int    `default:"6" help:"Count of parallel workers per directory."`
	Text       bool   `default:"false" help:"Print results in text instead of GUI."`
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
	for i := 0; i < CLI.Workers; i++ {
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

func walkDir(mapA, mapB *DirMap, dirA *dirtree.Dirent, diff *dirtree.Dirent) error {
	isDir := false
	dirA.ForChild(func(d *dirtree.Dirent) bool {
		isDir = true
		if err := walkDir(mapA, mapB, d, diff); err != nil {
			log.Fatalln(err)
		}
		return true
	})
	if isDir {
		return nil
	}
	h, ok := mapA.entryMap[dirA]
	if !ok {
		// this is a directory
		return nil
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
		parts := strings.Split(dirA.Path(), "/")
		curNode := diff
		for _, part := range parts {
			if part == "" {
				continue
			}
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
	}
	return nil
}

func hasChildren(d *dirtree.Dirent) bool {
	result := false
	d.ForChild(func(d *dirtree.Dirent) bool {
		result = true
		return false
	})
	return result
}

func pathToDirent(root *dirtree.Dirent, path string) *dirtree.Dirent {
	currNode := root
	for _, part := range append(strings.Split(path, "/")) {
		if part == "" {
			continue
		}
		currNode = currNode.Child(part)
		if currNode == nil {
			return nil
		}
	}
	return currNode
}

func expandPathToClosestNode(root *tview.TreeNode, path string, pageData []PageData) *tview.TreeNode {
	currNode := root
outer:
	for _, part := range append(strings.Split(path, "/")) {
		if part == "" {
			continue
		}
		if len(currNode.GetChildren()) == 0 {
			addHandler(currNode, pageData)
		}
		currNode.Expand()
		for _, child := range currNode.GetChildren() {
			if child.GetText() == part {
				currNode = child
				continue outer
			}
		}
		return currNode
	}
	return currNode
}

func printDir(dir *dirtree.Dirent) {
	sortedChildren := dir.List()
	if len(sortedChildren) > 0 {
		for _, childName := range sortedChildren {
			printDir(dir.Child(childName))
		}
		return
	}
	fmt.Println(dir.Path())
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
	diffA := dirtree.New("")
	if err := walkDir(dirA, dirB, dirA.root, diffA); err != nil {
		return err
	}
	diffB := dirtree.New("")
	if err := walkDir(dirB, dirA, dirB.root, diffB); err != nil {
		return err
	}
	log.Println("Done")
	if CLI.Text {
		fmt.Printf("========== Only in %s ==========\n", CLI.DirA)
		printDir(diffA)
		fmt.Printf("========== Only in %s ==========\n", CLI.DirB)
		printDir(diffB)
	} else {
		if err := renderUI(diffA, diffB); err != nil {
			return err
		}
	}
	return nil
}

type NodeReference struct {
	entry *dirtree.Dirent
	isDir bool
}

func addHandler(node *tview.TreeNode, pageData []PageData) {
	var dirs []*dirtree.Dirent
	var files []*dirtree.Dirent
	reference := node.GetReference().(NodeReference)
	for _, name := range reference.entry.List() {
		d := reference.entry.Child(name)
		if hasChildren(d) {
			dirs = append(dirs, d)
		} else {
			files = append(files, d)
		}
	}
	var combined []*dirtree.Dirent
	for _, dir := range dirs {
		combined = append(combined, dir)
	}
	for _, file := range files {
		combined = append(combined, file)
	}
	for _, entry := range combined {
		presentInBoth := true
		for _, data := range pageData {
			if pathToDirent(data.dirDiff, reference.entry.Path()+"/"+entry.String()) == nil {
				presentInBoth = false
				break
			}
		}
		isDir := hasChildren(entry)
		var color tcell.Color
		if presentInBoth {
			if isDir {
				color = tcell.ColorYellow
			} else {
				color = tcell.ColorBlue
			}
		} else {
			if isDir {
				color = tcell.ColorGreen
			} else {
				color = tcell.ColorWhite
			}
		}
		node.AddChild(tview.NewTreeNode(entry.String()).
			SetReference(NodeReference{entry, isDir}).
			SetExpanded(false).
			SetColor(color).
			SetSelectable(isDir))
	}
}

func selectHandler(node *tview.TreeNode, recurse bool, pageData []PageData) {
	if node.IsExpanded() {
		if recurse {
			collapseAll(node)
		} else {
			node.Collapse()
		}
	} else {
		children := node.GetChildren()
		if len(children) == 0 {
			addHandler(node, pageData)
		}
		node.SetExpanded(true)
		if recurse {
			for _, child := range node.GetChildren() {
				selectHandler(child, true, pageData)
			}
		}
	}
}

// https://github.com/rivo/tview/pull/848
func collapseAll(node *tview.TreeNode) {
	node.Walk(func(n, parent *tview.TreeNode) bool {
		n.SetExpanded(false)
		return true
	})
}

func expandAtDepth(node *tview.TreeNode, depth int, pageData []PageData) {
	children := node.GetChildren()
	if len(children) == 0 {
		addHandler(node, pageData)
	}
	if depth < 1 {
		collapseAll(node)
	} else {
		node.SetExpanded(true)
		for _, child := range children {
			expandAtDepth(child, depth-1, pageData)
		}
	}
}

type PageData struct {
	pageName string
	dirPath  string
	dirDiff  *dirtree.Dirent
}

func renderUI(diffA *dirtree.Dirent, diffB *dirtree.Dirent) error {
	app := tview.NewApplication()
	pageData := []PageData{
		{"1", CLI.DirA, diffA},
		{"2", CLI.DirB, diffB},
	}
	pages := tview.NewPages()
	for _, data := range pageData {
		root := tview.NewTreeNode(data.dirPath).
			SetColor(tcell.ColorRed).
			SetReference(NodeReference{data.dirDiff, true})
		addHandler(root, pageData)
		pages.AddPage(data.pageName, tview.NewTreeView().
			SetRoot(root).
			SetCurrentNode(root), true, false)
	}
	cellSize := 25
	info := tview.NewGrid().
		SetRows(1, 1).
		SetColumns(cellSize, cellSize, cellSize).
		AddItem(tview.NewTextView().SetText("[q] quit"), 0, 0, 1, 1, 0, 0, false).
		AddItem(tview.NewTextView().SetText("[space] switch views"), 0, 1, 1, 1, 0, 0, false).
		AddItem(tview.NewTextView().SetText("[tab] focus in other view"), 0, 2, 1, 1, 0, 0, false).
		AddItem(tview.NewTextView().SetText("[F1] toggle all"), 1, 0, 1, 1, 0, 0, false).
		AddItem(tview.NewTextView().SetText("[1-9] toggle at depth"), 1, 1, 1, 2, 0, 0, false)
	layout := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(pages, 0, 1, true).
		AddItem(tview.NewFlex().
			SetDirection(tview.FlexColumn).
			AddItem(info, cellSize*3, 1, false).
			AddItem(tview.NewTextView().SetText(" "), 0, 1, false),
			2, 1, false)
	layout.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		pageName, page := pages.GetFrontPage()
		tree := page.(*tview.TreeView)
		switch event.Key() {
		case tcell.KeyTab:
			node := tree.GetCurrentNode()
			if pageName == "1" {
				pages.SwitchToPage("2")
			} else {
				pages.SwitchToPage("1")
			}
			_, newPage := pages.GetFrontPage()
			newTree := newPage.(*tview.TreeView)
			newNode := expandPathToClosestNode(newTree.GetRoot(), node.GetReference().(NodeReference).entry.Path(), pageData)
			newTree.SetCurrentNode(newNode)
			return nil
		case tcell.KeyF1:
			selectHandler(tree.GetCurrentNode(), true, pageData)
			return nil
		case tcell.KeyLeft:
			node := tree.GetCurrentNode()
			if node.IsExpanded() {
				node.Collapse()
			} else {
				parentNode := expandPathToClosestNode(
					tree.GetRoot(), filepath.Join(node.GetReference().(NodeReference).entry.Path(), ".."), pageData)
				tree.SetCurrentNode(parentNode)
			}
			return nil
		case tcell.KeyRight:
			node := tree.GetCurrentNode()
			if !tree.GetCurrentNode().IsExpanded() {
				selectHandler(node, false, pageData)
			} else {
				children := node.GetChildren()
				for _, child := range children {
					if child.GetReference().(NodeReference).isDir {
						tree.SetCurrentNode(child)
						break
					}
				}
			}
			return nil
		case tcell.KeyRune:
			if event.Rune() >= '1' && event.Rune() <= '9' {
				depth, err := strconv.ParseInt(string(event.Rune()), 10, 64)
				if err != nil {
					log.Fatalln(err)
				}
				expandAtDepth(tree.GetCurrentNode(), int(depth), pageData)
			} else if event.Rune() == 'q' {
				app.Stop()
			} else if event.Rune() == ' ' {
				if pageName == "1" {
					pages.SwitchToPage("2")
				} else {
					pages.SwitchToPage("1")
				}
			}
			return nil
		}
		return event
	})
	app.SetRoot(layout, true)
	pages.SwitchToPage("1")
	if err := app.Run(); err != nil {
		return err
	}
	return nil
}
