package main

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
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
	excludeDirs []string
}

func NewFileIndex(cwd string, excludeDirs []string) *FileIndex {
	return &FileIndex{
		files:       make(map[string][]FileInfo),
		readyChan:   make(chan struct{}),
		cwd:         cwd,
		excludeDirs: excludeDirs,
	}
}

func (idx *FileIndex) Start(ctx context.Context) {
	gitDir := filepath.Join(idx.cwd, ".git")
	if info, err := os.Stat(gitDir); err == nil && info.IsDir() {
		idx.buildFromGit(ctx)
	} else {
		idx.buildFromFilesystem(ctx)
	}

	idx.mu.Lock()
	idx.ready = true
	idx.mu.Unlock()
	close(idx.readyChan)
}

func (idx *FileIndex) buildFromGit(ctx context.Context) {
	cmd := exec.CommandContext(ctx, "git", "ls-files")
	cmd.Dir = idx.cwd
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return
	}
	if err := cmd.Start(); err != nil {
		return
	}

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			cmd.Process.Kill()
			return
		default:
		}

		relPath := scanner.Text()
		absPath := filepath.Join(idx.cwd, relPath)
		info, err := os.Stat(absPath)
		if err != nil || info.IsDir() {
			continue
		}

		basename := filepath.Base(relPath)
		idx.mu.Lock()
		idx.files[basename] = append(idx.files[basename], FileInfo{
			path:  absPath,
			mtime: info.ModTime(),
		})
		idx.mu.Unlock()
	}

	cmd.Wait()
}

func (idx *FileIndex) buildFromFilesystem(ctx context.Context) {
	excludeSet := make(map[string]bool)
	for _, d := range idx.excludeDirs {
		excludeSet[d] = true
	}

	filepath.WalkDir(idx.cwd, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}

		select {
		case <-ctx.Done():
			return filepath.SkipAll
		default:
		}

		if d.IsDir() {
			if excludeSet[d.Name()] {
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
