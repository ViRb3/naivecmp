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
	"sort"
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
	UsePath    bool   `default:"false" help:"Use file directory path (default false)."`
	Workers    int    `default:"6" help:"Count of parallel workers per directory."`
	Text       bool   `default:"false" help:"Print results in text instead of GUI."`
	FileCount  bool   `default:"true" help:"Print file counts in GUI mode (default true)."`
	Debug      bool   `default:"false" help:"Print debug output, useful to troubleshoot issues."`
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

const FileCountPlaceHolder = "[?] "

func hash(filePath string, info fs.FileInfo) uint64 {
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
	if CLI.UsePath {
		fileDir := filepath.Dir(filePath) + string(filepath.Separator)
		data = append(data, []byte(fileDir)...)
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
	h := hash(scanEntry.path, info)
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

func getPartsToNode(root *tview.TreeNode, node *tview.TreeNode, pageData []PageData) []*tview.TreeNode {
	var parts []*tview.TreeNode
	walkToClosestNode(root, node.GetReference().(*NodeReference).entry.Path(),
		func(node *tview.TreeNode) {
			parts = append(parts, node)
		}, pageData)
	return parts
}

func walkToClosestNode(root *tview.TreeNode, path string, callback func(node *tview.TreeNode), pageData []PageData) *tview.TreeNode {
	currNode := root
	callback(currNode)
outer:
	for _, part := range append(strings.Split(path, "/")) {
		if part == "" {
			continue
		}
		if len(currNode.GetChildren()) == 0 {
			addHandler(root, currNode, pageData)
		}
		for _, child := range currNode.GetChildren() {
			if child.GetReference() != nil && child.GetReference().(*NodeReference).entry.String() == part {
				currNode = child
				callback(currNode)
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
		if CLI.Debug {
			fmt.Printf("========== Debug for %s ==========\n", CLI.DirA)
			printDebug(dirA)
			fmt.Printf("========== Debug for %s ==========\n", CLI.DirB)
			printDebug(dirB)
		}
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

func printDebug(dirMap *DirMap) {
	entries := make([]*dirtree.Dirent, 0, len(dirMap.entryMap))
	for entry := range dirMap.entryMap {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Path() < entries[j].Path()
	})
	for _, entry := range entries {
		fmt.Printf("%s %d\n", entry.Path(), dirMap.entryMap[entry])
	}
}

type NodeReference struct {
	entry     *dirtree.Dirent
	isDir     bool
	fileCount int
}

func addHandler(root *tview.TreeNode, node *tview.TreeNode, pageData []PageData) {
	var dirs []*dirtree.Dirent
	var files []*dirtree.Dirent
	if node.GetReference() == nil {
		return
	}
	reference := node.GetReference().(*NodeReference)
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
		name := entry.String()
		if CLI.FileCount && isDir {
			name = FileCountPlaceHolder + name
		}
		node.AddChild(tview.NewTreeNode(tview.Escape(name)).
			SetReference(&NodeReference{entry, isDir, 1}).
			SetExpanded(false).
			SetColor(color).
			SetSelectable(isDir))
	}
	updateFileCounts(root, node, pageData)
}

func updateFileCounts(root *tview.TreeNode, node *tview.TreeNode, pageData []PageData) {
	childrenLen := len(node.GetChildren())
	if !CLI.FileCount {
		return
	}
	reference := node.GetReference().(*NodeReference)
	if !reference.isDir {
		return
	}
	reference.fileCount = childrenLen
	updateFileCountText(node, childrenLen)
	parts := getPartsToNode(root, node, pageData)
	for i := len(parts) - 1; i >= 0; i-- {
		reference := parts[i].GetReference().(*NodeReference)
		reference.fileCount = 0
		for _, child := range parts[i].GetChildren() {
			if child.GetReference() != nil {
				reference.fileCount += child.GetReference().(*NodeReference).fileCount
			}
		}
		updateFileCountText(parts[i], reference.fileCount)
	}
}

func updateFileCountText(node *tview.TreeNode, newCount int) {
	node.SetText(fmt.Sprintf("[%d] %s", newCount, node.GetText()[strings.Index(node.GetText(), "] ")+2:]))
}

func selectHandler(root *tview.TreeNode, node *tview.TreeNode, recurse bool, pageData []PageData) {
	if node.IsExpanded() {
		if recurse {
			node.CollapseAll()
		} else {
			node.Collapse()
		}
	} else {
		children := node.GetChildren()
		if len(children) == 0 {
			addHandler(root, node, pageData)
		}
		node.SetExpanded(true)
		if recurse {
			for _, child := range node.GetChildren() {
				selectHandler(root, child, true, pageData)
			}
		}
	}
}

func expandAtDepth(root *tview.TreeNode, node *tview.TreeNode, depth int, pageData []PageData) {
	if depth < 1 {
		node.CollapseAll()
	} else {
		if len(node.GetChildren()) == 0 {
			addHandler(root, node, pageData)
		}
		node.SetExpanded(true)
		for _, child := range node.GetChildren() {
			expandAtDepth(root, child, depth-1, pageData)
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
		name := data.dirPath
		if CLI.FileCount {
			name = FileCountPlaceHolder + name
		}
		root := tview.NewTreeNode(tview.Escape(name)).
			SetColor(tcell.ColorRed).
			SetReference(&NodeReference{data.dirDiff, true, 1})
		addHandler(root, root, pageData)
		// this is a dummy directory, always last, so the user can select it and see any files that may otherwise be out of view
		root.AddChild(tview.NewTreeNode(tview.Escape(" ")).
			SetExpanded(false).
			SetReference(nil).
			SetSelectable(true))
		pages.AddPage(data.pageName, tview.NewTreeView().
			SetRoot(root).
			SetCurrentNode(root), true, false)
	}
	cellSize := 25
	var columns []int
	for i := 0; i < 4; i++ {
		columns = append(columns, cellSize)
	}
	info := tview.NewGrid().
		SetRows(1, 1).
		SetColumns(columns...).
		AddItem(tview.NewTextView().SetText("[q] quit"), 0, 0, 1, 1, 0, 0, false).
		AddItem(tview.NewTextView().SetText("[space] switch views"), 0, 1, 1, 1, 0, 0, false).
		AddItem(tview.NewTextView().SetText("[shift+] free move"), 0, 2, 1, 1, 0, 0, false).
		AddItem(tview.NewTextView().SetText("[tab] focus in other view"), 0, 3, 1, 1, 0, 0, false).
		AddItem(tview.NewTextView().SetText("[F1] toggle all"), 1, 0, 1, 1, 0, 0, false).
		AddItem(tview.NewTextView().SetText("[1-9] toggle at depth"), 1, 1, 1, 1, 0, 0, false).
		AddItem(tview.NewTextView().SetText("[d] hide from view"), 1, 2, 1, 2, 0, 0, false)
	layout := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(pages, 0, 1, true).
		AddItem(tview.NewFlex().
			SetDirection(tview.FlexColumn).
			AddItem(info, cellSize*len(columns), 1, false).
			AddItem(tview.NewTextView().SetText(" "), 0, 1, false),
			2, 1, false)
	var lastSelection *tview.TreeNode
	layout.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		pageName, page := pages.GetFrontPage()
		tree := page.(*tview.TreeView)
		root := tree.GetRoot()
		node := tree.GetCurrentNode()
		// restrict keys allowed for "free move"
		if node == nil {
			switch event.Key() {
			case tcell.KeyUp:
				fallthrough
			case tcell.KeyDown:
				fallthrough
			case tcell.KeyPgUp:
				fallthrough
			case tcell.KeyPgDn:
				break
			default:
				return nil
			}
		}
		// restrict keys allowed for "dummy directory"
		if node != nil && node.GetReference() == nil {
			switch event.Key() {
			case tcell.KeyUp:
				fallthrough
			case tcell.KeyDown:
				fallthrough
			case tcell.KeyPgUp:
				fallthrough
			case tcell.KeyPgDn:
				break
			case tcell.KeyRune:
				switch event.Rune() {
				case 'q':
					fallthrough
				case ' ':
					break
				default:
					return nil
				}
			default:
				return nil
			}
		}
		switch event.Key() {
		case tcell.KeyUp:
			fallthrough
		case tcell.KeyDown:
			fallthrough
		case tcell.KeyPgUp:
			fallthrough
		case tcell.KeyPgDn:
			if event.Modifiers()&tcell.ModShift > 0 {
				if tree.GetCurrentNode() != nil {
					lastSelection = tree.GetCurrentNode()
				}
				tree.SetCurrentNode(nil)
			} else if tree.GetCurrentNode() == nil && lastSelection != nil {
				tree.SetCurrentNode(lastSelection)
			}
			return event
		case tcell.KeyTab:
			if pageName == "1" {
				pages.SwitchToPage("2")
			} else {
				pages.SwitchToPage("1")
			}
			_, newPage := pages.GetFrontPage()
			newTree := newPage.(*tview.TreeView)
			newNode := walkToClosestNode(
				newTree.GetRoot(), node.GetReference().(*NodeReference).entry.Path(),
				func(node *tview.TreeNode) {
					node.Expand()
				},
				pageData)
			newTree.SetCurrentNode(newNode)
		case tcell.KeyF1:
			selectHandler(root, node, true, pageData)
		case tcell.KeyLeft:
			if node.IsExpanded() {
				node.Collapse()
			} else if parts := getPartsToNode(root, node, pageData); len(parts) > 1 {
				parentNode := parts[len(parts)-2]
				tree.SetCurrentNode(parentNode)
			}
		case tcell.KeyRight:
			if !node.IsExpanded() {
				selectHandler(root, node, false, pageData)
			} else {
				children := node.GetChildren()
				for _, child := range children {
					if child.GetReference() != nil && child.GetReference().(*NodeReference).isDir {
						tree.SetCurrentNode(child)
						break
					}
				}
			}
		case tcell.KeyRune:
			if event.Rune() >= '1' && event.Rune() <= '9' {
				depth, err := strconv.ParseInt(string(event.Rune()), 10, 64)
				if err != nil {
					log.Fatalln(err)
				}
				expandAtDepth(root, node, int(depth), pageData)
			} else if event.Rune() == 'q' {
				app.Stop()
			} else if event.Rune() == ' ' {
				if pageName == "1" {
					pages.SwitchToPage("2")
				} else {
					pages.SwitchToPage("1")
				}
			} else if event.Rune() == 'd' {
				if parts := getPartsToNode(root, node, pageData); len(parts) > 1 {
					parentNode := parts[len(parts)-2]
					children := parentNode.GetChildren()
					found := false
					var nextNode *tview.TreeNode
					for _, child := range children {
						if child == node {
							found = true
							continue
						}
						if child.GetReference() != nil && child.GetReference().(*NodeReference).isDir {
							nextNode = child
							if found {
								break
							}
						}
					}
					if nextNode == nil {
						nextNode = parentNode
					}
					tree.SetCurrentNode(nextNode)
					parentNode.RemoveChild(node)
					updateFileCounts(root, nextNode, pageData)
				}
			}
		}
		return nil
	})
	app.SetRoot(layout, true)
	pages.SwitchToPage("1")
	if err := app.Run(); err != nil {
		return err
	}
	return nil
}
