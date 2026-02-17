package main

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/mash/osc8wrap/symwalk"
)

type FileInfo struct {
	path  string
	mtime time.Time
}

type FileIndex struct {
	mu          sync.RWMutex
	ready       bool
	files       map[string][]FileInfo
	readyChan   chan struct{}
	cwd         string
	excludeSet  map[string]bool
	ignoredDirs map[string]bool
	watcher     *fsnotify.Watcher
}

func NewFileIndex(cwd string, excludeDirs []string) *FileIndex {
	excludeSet := make(map[string]bool)
	for _, d := range excludeDirs {
		excludeSet[d] = true
	}
	return &FileIndex{
		files:      make(map[string][]FileInfo),
		readyChan:  make(chan struct{}),
		cwd:        cwd,
		excludeSet: excludeSet,
	}
}

func (idx *FileIndex) Start(ctx context.Context) {
	idx.ignoredDirs = loadGitIgnoredDirs(ctx, idx.cwd)
	idx.buildFromFilesystem(ctx)

	idx.mu.Lock()
	idx.ready = true
	idx.mu.Unlock()
	close(idx.readyChan)

	idx.startWatcher(ctx)
}

func (idx *FileIndex) buildFromFilesystem(ctx context.Context) {
	_ = symwalk.WalkDir(idx.cwd, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return filepath.SkipAll
		default:
		}
		if d.IsDir() {
			if idx.isIgnoredDir(path) {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		basename := filepath.Base(path)
		idx.mu.Lock()
		idx.files[basename] = append(idx.files[basename], FileInfo{
			path:  path,
			mtime: info.ModTime(),
		})
		idx.mu.Unlock()
		return nil
	})
}

func (idx *FileIndex) Wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-idx.readyChan:
		return nil
	}
}

func (idx *FileIndex) Resolve(path string) string {
	idx.mu.RLock()
	ready := idx.ready
	idx.mu.RUnlock()
	if !ready {
		return ""
	}

	basename := filepath.Base(path)
	idx.mu.RLock()
	candidates := idx.files[basename]
	idx.mu.RUnlock()

	if len(candidates) == 0 {
		return ""
	}

	if strings.Contains(path, "/") {
		var filtered []FileInfo
		for _, c := range candidates {
			if strings.HasSuffix(c.path, "/"+path) {
				filtered = append(filtered, c)
			}
		}
		if len(filtered) > 0 {
			candidates = filtered
		}
	}

	if len(candidates) == 1 {
		return candidates[0].path
	}

	var newest FileInfo
	for _, c := range candidates {
		if c.mtime.After(newest.mtime) {
			newest = c
		}
	}
	return newest.path
}

func (idx *FileIndex) startWatcher(ctx context.Context) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return
	}
	idx.watcher = watcher

	idx.watchDirRecursive(idx.cwd)

	go idx.watchLoop(ctx)
}

func (idx *FileIndex) watchDirRecursive(root string) {
	_ = symwalk.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if idx.isIgnoredDir(path) {
			return filepath.SkipDir
		}
		_ = idx.watcher.Add(path)
		return nil
	})
}

func (idx *FileIndex) watchLoop(ctx context.Context) {
	defer idx.watcher.Close() //nolint:errcheck

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-idx.watcher.Events:
			if !ok {
				return
			}
			idx.handleEvent(event)
		case _, ok := <-idx.watcher.Errors:
			if !ok {
				return
			}
		}
	}
}

func (idx *FileIndex) handleEvent(event fsnotify.Event) {
	if idx.isIgnoredDir(event.Name) {
		return
	}

	switch {
	case event.Has(fsnotify.Create):
		idx.handleCreate(event.Name)
	case event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename):
		idx.handleRemove(event.Name)
	}
}

func (idx *FileIndex) handleCreate(path string) {
	if idx.isIgnoredDir(path) {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		return
	}

	if info.IsDir() {
		idx.watchDirRecursive(path)
		idx.indexDir(path)
		return
	}

	idx.addFile(path, info.ModTime())
}

func (idx *FileIndex) handleRemove(path string) {
	basename := filepath.Base(path)

	idx.mu.Lock()
	defer idx.mu.Unlock()

	files := idx.files[basename]
	for i, f := range files {
		if f.path == path {
			idx.files[basename] = append(files[:i], files[i+1:]...)
			break
		}
	}
}

func (idx *FileIndex) indexDir(dir string) {
	_ = symwalk.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if idx.isIgnoredDir(path) {
				return filepath.SkipDir
			}
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		idx.addFile(path, info.ModTime())
		return nil
	})
}

func (idx *FileIndex) isIgnoredDir(path string) bool {
	return idx.excludeSet[filepath.Base(path)] || idx.ignoredDirs[path]
}

func loadGitIgnoredDirs(ctx context.Context, cwd string) map[string]bool {
	cmd := exec.CommandContext(ctx, "git", "ls-files", "-oi", "--exclude-standard", "--directory")
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	dirs := make(map[string]bool)
	for line := range strings.SplitSeq(string(out), "\n") {
		line = strings.TrimRight(line, "/\r\n ")
		if line == "" {
			continue
		}
		dirs[filepath.Join(cwd, line)] = true
	}
	return dirs
}

func (idx *FileIndex) addFile(path string, mtime time.Time) {
	basename := filepath.Base(path)

	idx.mu.Lock()
	defer idx.mu.Unlock()

	idx.files[basename] = append(idx.files[basename], FileInfo{
		path:  path,
		mtime: mtime,
	})
}
