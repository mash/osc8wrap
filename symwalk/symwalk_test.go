package symwalk_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/mash/osc8wrap/symwalk"
)

func collectPaths(t *testing.T, root string) []string {
	t.Helper()
	var got []string
	err := symwalk.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		got = append(got, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	return got
}

func TestWalkDir_SymlinkType(t *testing.T) {
	tmp := t.TempDir()
	os.MkdirAll(filepath.Join(tmp, "real"), 0o755)
	os.WriteFile(filepath.Join(tmp, "real", "data.txt"), []byte("."), 0o644)
	os.Symlink(filepath.Join(tmp, "real"), filepath.Join(tmp, "linkdir"))
	os.Symlink(filepath.Join(tmp, "real", "data.txt"), filepath.Join(tmp, "linkfile"))

	types := make(map[string]fs.FileMode)
	symwalk.WalkDir(tmp, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(tmp, path)
		types[rel] = d.Type()
		return nil
	})

	wantSymlink := []string{"linkdir", "linkfile"}
	for _, name := range wantSymlink {
		if types[name]&os.ModeSymlink == 0 {
			t.Errorf("%s should have ModeSymlink, got %v", name, types[name])
		}
	}
	noSymlink := []string{".", "real", "real/data.txt"}
	for _, name := range noSymlink {
		if types[name]&os.ModeSymlink != 0 {
			t.Errorf("%s should not have ModeSymlink, got %v", name, types[name])
		}
	}
}

func TestWalkDir(t *testing.T) {
	tests := []struct {
		name  string
		setup func(tmp string)
		want  []string
	}{
		{
			name: "regular files",
			setup: func(tmp string) {
				os.MkdirAll(filepath.Join(tmp, "a", "b"), 0o755)
				os.WriteFile(filepath.Join(tmp, "a", "file1.txt"), []byte("."), 0o644)
				os.WriteFile(filepath.Join(tmp, "a", "b", "file2.txt"), []byte("."), 0o644)
			},
			want: []string{".", "a", "a/b", "a/b/file2.txt", "a/file1.txt"},
		},
		{
			name: "symlink directory",
			setup: func(tmp string) {
				os.MkdirAll(filepath.Join(tmp, "realdir"), 0o755)
				os.WriteFile(filepath.Join(tmp, "realdir", "hello.txt"), []byte("."), 0o644)
				os.Symlink(filepath.Join(tmp, "realdir"), filepath.Join(tmp, "linkdir"))
			},
			want: []string{
				".",
				"linkdir", "linkdir/hello.txt",
				"realdir", "realdir/hello.txt",
			},
		},
		{
			name: "symlink path prefix",
			setup: func(tmp string) {
				os.MkdirAll(filepath.Join(tmp, "real", "sub"), 0o755)
				os.WriteFile(filepath.Join(tmp, "real", "sub", "data.txt"), []byte("."), 0o644)
				os.Symlink(filepath.Join(tmp, "real"), filepath.Join(tmp, "link"))
			},
			want: []string{
				".",
				"link", "link/sub", "link/sub/data.txt",
				"real", "real/sub", "real/sub/data.txt",
			},
		},
		{
			name: "symlink loop",
			setup: func(tmp string) {
				os.MkdirAll(filepath.Join(tmp, "a"), 0o755)
				os.MkdirAll(filepath.Join(tmp, "b"), 0o755)
				os.WriteFile(filepath.Join(tmp, "a", "fa.txt"), []byte("."), 0o644)
				os.WriteFile(filepath.Join(tmp, "b", "fb.txt"), []byte("."), 0o644)
				os.Symlink(filepath.Join(tmp, "b"), filepath.Join(tmp, "a", "to_b"))
				os.Symlink(filepath.Join(tmp, "a"), filepath.Join(tmp, "b", "to_a"))
			},
			// a/to_b/to_a/to_b and b/to_a point to already-visited dirs
			// and are silently skipped.
			want: []string{
				".",
				"a", "a/fa.txt",
				"a/to_b", "a/to_b/fb.txt", "a/to_b/to_a", "a/to_b/to_a/fa.txt",
				"b", "b/fb.txt",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := t.TempDir()
			tt.setup(tmp)

			got := collectPaths(t, tmp)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("walked paths mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestWalkDir_SkipDir(t *testing.T) {
	tmp := t.TempDir()
	os.MkdirAll(filepath.Join(tmp, "real"), 0o755)
	os.WriteFile(filepath.Join(tmp, "real", "secret.txt"), []byte("."), 0o644)
	os.Symlink(filepath.Join(tmp, "real"), filepath.Join(tmp, "link"))

	tests := []struct {
		name string
		skip string
		want []string
	}{
		{
			name: "skip symlink",
			skip: "link",
			want: []string{".", "real", "real/secret.txt"},
		},
		{
			// "link" < "real" lexically, so the symlink is visited first.
			// Skipping "real" still leaves the contents reachable via "link".
			name: "skip real dir",
			skip: "real",
			want: []string{".", "link", "link/secret.txt"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			err := symwalk.WalkDir(tmp, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() && d.Name() == tt.skip {
					return filepath.SkipDir
				}
				rel, _ := filepath.Rel(tmp, path)
				got = append(got, rel)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}

			sort.Strings(got)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("walked paths mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestWalkDir_SkipAll(t *testing.T) {
	tmp := t.TempDir()
	os.MkdirAll(filepath.Join(tmp, "real"), 0o755)
	os.WriteFile(filepath.Join(tmp, "real", "a.txt"), []byte("."), 0o644)
	os.WriteFile(filepath.Join(tmp, "real", "b.txt"), []byte("."), 0o644)
	// "link" < "real" lexically, so the symlink is visited first and
	// SkipAll fires inside it before "real" is ever reached.
	os.Symlink(filepath.Join(tmp, "real"), filepath.Join(tmp, "link"))

	var got []string
	err := symwalk.WalkDir(tmp, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return filepath.SkipAll
		}
		rel, _ := filepath.Rel(tmp, path)
		got = append(got, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	sort.Strings(got)
	want := []string{".", "link"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("walked paths mismatch (-want +got):\n%s", diff)
	}
}
