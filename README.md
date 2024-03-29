## naivecmp

> Compare directories by fuzzy-matching file attributes without checking contents.

You are in a good habit of making periodic backups. After a certain point, you may want to clear your oldest backup to free space for new ones. But how do you know if something important wasn't accidentally deleted in the meantime? If you don't know what to look for, you may only find out years later, after this data is gone from all backups.

naivecmp aims to solve this problem by providing you with a very fast way to compare backups and/or your current state. The algorithm is able to track files even after they have been moved and/or renamed, which greatly reduces noise. It works by hashing files based on their file attributes (size + modification time), rather than name or content.

### Disclaimer

While this tool has been perfectly accurate in my own tests, it is still fundamentally flawed (thus the "naive" name) — you may get false negatives if two different files have the same file attributes, or if the content of a file was changed without altering its size or modification time. In case there are hash collisions, naivecmp will fall back to comparing the full path, but even then, this is not a 100% guarantee the files are identical. Therefore, do not rely on this tool for mission-critical decisions. Use it only as a quick way to check the "most likely" changes between two directories.

### Features

- Bi-directional diff
- Very fast (10TB+ hard drive array in less than 20 seconds on a Raspberry Pi 4)
- Low memory usage (less than 300MB for scanning 10TB+ of data)
- Configurable matching conditions
- Tracks files even when they were moved and/or renamed
- Parallel directory scanning
- TreeView GUI and text mode

### Usage

```bash
Usage: naivecmp <dir-a> <dir-b>

Compare directories by fuzzy-matching file attributes without checking contents.

Arguments:
  <dir-a>    Directory A.
  <dir-b>    Directory B.

Flags:
  -h, --help            Show context-sensitive help.
      --use-mod-time    Use file mod time (default true).
      --use-size        Use file size (default true).
      --use-mode        Use file mode (default false).
      --use-name        Use file name even when there is no collision (default false).
      --use-path        Use file directory path (default false).
      --workers=6       Count of parallel workers per directory.
      --text            Print results in text instead of GUI.
      --file-count      Print file counts in GUI mode (default true).
```

In GUI mode (default), you will be presented with a TreeView that lets you explore the differences in each directory.

| Hotkeys                   | Colors                                   |
| ------------------------- | ---------------------------------------- |
| [q] quit                  | [white] new file                         |
| [space] switch views      | [green] directory                        |
| [tab] focus in other view | [yellow] directory present in both sides |
| [shift+] free move        | [blue] file present in both sides        |
| [F1] toggle all           |                                          |
| [1-9] toggle at depth     |                                          |
| [d] hide from view        |                                          |

In text mode, differences will be printed to stdout, while status updates will go to stderr.
