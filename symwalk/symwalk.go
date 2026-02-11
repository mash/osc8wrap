package symwalk

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// errStopWalk propagates filepath.SkipAll across recursive calls.
var errStopWalk = errors.New("stop walk")

// WalkDir is like filepath.WalkDir but follows symbolic links to directories.
// Symlink loops are prevented by tracking visited real paths.
// When a symlink to a directory is encountered, the callback receives a DirEntry
// with IsDir() returning true. Returning filepath.SkipDir skips that directory.
// Paths passed to the callback preserve the original symlink-based prefix.
func WalkDir(root string, fn fs.WalkDirFunc) error {
	visited := make(map[string]bool)
	err := walkDir(root, visited, fn, false)
	if errors.Is(err, errStopWalk) {
		return nil
	}
	return err
}

func walkDir(root string, visited map[string]bool, fn fs.WalkDirFunc, followedSymlink bool) error {
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fn(root, nil, err)
	}
	if visited[real] {
		return nil
	}
	visited[real] = true

	isSymlink := root != real

	return filepath.WalkDir(real, func(path string, d os.DirEntry, err error) error {
		displayPath := path
		if isSymlink {
			rel, relErr := filepath.Rel(real, path)
			if relErr == nil {
				displayPath = filepath.Join(root, rel)
			}
		}

		if err != nil {
			return fn(displayPath, d, err)
		}

		if d.Type()&os.ModeSymlink != 0 {
			info, statErr := os.Stat(path)
			if statErr != nil {
				return fn(displayPath, d, statErr)
			}
			if info.IsDir() {
				if err := walkDir(displayPath, visited, fn, true); err != nil {
					return err
				}
				return nil
			}
			return fn(displayPath, d, nil)
		}

		// Rename the root DirEntry so d.Name() returns the symlink name.
		if isSymlink && path == real {
			info, infoErr := d.Info()
			if infoErr == nil {
				return handleCbErr(fn(displayPath, &dirEntry{name: filepath.Base(root), info: info, symlink: followedSymlink}, nil))
			}
		}

		return handleCbErr(fn(displayPath, d, nil))
	})
}

func handleCbErr(err error) error {
	if errors.Is(err, filepath.SkipAll) {
		return errStopWalk
	}
	return err
}

type dirEntry struct {
	name    string
	info    fs.FileInfo
	symlink bool
}

func (e *dirEntry) Name() string { return e.name }
func (e *dirEntry) IsDir() bool  { return true }
func (e *dirEntry) Type() fs.FileMode {
	if e.symlink {
		return fs.ModeDir | fs.ModeSymlink
	}
	return fs.ModeDir
}
func (e *dirEntry) Info() (fs.FileInfo, error) { return e.info, nil }
