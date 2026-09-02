package main

import (
	"path/filepath"
	"testing"
)

func TestParseGitHubSlug(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{"ssh shorthand", "git@github.com:mash/osc8wrap.git", "mash/osc8wrap"},
		{"ssh shorthand no .git", "git@github.com:mash/osc8wrap", "mash/osc8wrap"},
		{"ssh url", "ssh://git@github.com/mash/osc8wrap.git", "mash/osc8wrap"},
		{"https url", "https://github.com/mash/osc8wrap.git", "mash/osc8wrap"},
		{"https url no .git", "https://github.com/mash/osc8wrap", "mash/osc8wrap"},
		{"https url trailing slash", "https://github.com/mash/osc8wrap/", "mash/osc8wrap"},
		{"non-github host", "https://gitlab.com/mash/osc8wrap.git", ""},
		{"github enterprise host", "https://github.example.com/mash/osc8wrap.git", ""},
		{"garbage", "not a url", ""},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseGitHubSlug(tt.url); got != tt.want {
				t.Errorf("parseGitHubSlug(%q) = %q, want %q", tt.url, got, tt.want)
			}
		})
	}
}

func TestOriginURL(t *testing.T) {
	tests := []struct {
		name   string
		config string // "" writes no config file at all
		want   string
	}{
		{
			name: "origin present",
			config: "[core]\n\trepositoryformatversion = 0\n" +
				"[remote \"origin\"]\n\turl = git@github.com:mash/osc8wrap.git\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n" +
				"[branch \"main\"]\n\tremote = origin\n\tmerge = refs/heads/main\n",
			want: "git@github.com:mash/osc8wrap.git",
		},
		{
			name:   "other remote only",
			config: "[remote \"upstream\"]\n\turl = git@github.com:other/repo.git\n",
			want:   "",
		},
		{
			name:   "no remotes at all",
			config: "[core]\n\trepositoryformatversion = 0\n",
			want:   "",
		},
		{
			name:   "missing config file",
			config: "",
			want:   "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			if tt.config != "" {
				gitTestWriteFile(t, filepath.Join(root, ".git", "config"), tt.config)
			}
			if got := originURL(root); got != tt.want {
				t.Errorf("originURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGitHubRepo_Slug(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, tmp string) string // returns cwd
		want  string
	}{
		{
			name: "github origin resolves the slug",
			setup: func(t *testing.T, tmp string) string {
				root := makeGitMainCheckout(t, filepath.Join(tmp, "repo"), "ref: refs/heads/main\n")
				gitTestWriteFile(t, filepath.Join(root, ".git", "config"),
					"[remote \"origin\"]\n\turl = https://github.com/mash/osc8wrap.git\n")
				return root
			},
			want: "mash/osc8wrap",
		},
		{
			name: "no origin remote",
			setup: func(t *testing.T, tmp string) string {
				root := makeGitMainCheckout(t, filepath.Join(tmp, "repo"), "ref: refs/heads/main\n")
				return root
			},
			want: "",
		},
		{
			name: "not a git repository",
			setup: func(t *testing.T, tmp string) string {
				return t.TempDir()
			},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := t.TempDir()
			cwd := tt.setup(t, tmp)
			repo := newGitHubRepo(cwd)
			if got := repo.Slug(); got != tt.want {
				t.Errorf("Slug() = %q, want %q", got, tt.want)
			}
			// Slug() is cached: a second call must return the same answer.
			if got := repo.Slug(); got != tt.want {
				t.Errorf("Slug() (cached) = %q, want %q", got, tt.want)
			}
		})
	}
}
