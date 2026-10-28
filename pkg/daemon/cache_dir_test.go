package daemon

import (
	"context"
	"path/filepath"
	"testing"
)

func TestPackageCacheDirResolution(t *testing.T) {
	t.Parallel()

	dataDir := "/var/lib/grain/data"
	sandboxDir := "/var/lib/grain/scratch"
	customDir := "/mnt/storage/custom-packages"

	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{
			name: "fallback to dataDir when sandboxDir and packageCacheDir are empty",
			cfg: Config{
				DataDir: dataDir,
			},
			want: filepath.Join(dataDir, "cache", "packages"),
		},
		{
			name: "default to sandboxDir when sandboxDir is set",
			cfg: Config{
				DataDir:    dataDir,
				SandboxDir: sandboxDir,
			},
			want: filepath.Join(sandboxDir, "cache", "packages"),
		},
		{
			name: "explicit packageCacheDir overrides sandboxDir and dataDir",
			cfg: Config{
				DataDir:         dataDir,
				SandboxDir:      sandboxDir,
				PackageCacheDir: customDir,
			},
			want: customDir,
		},
		{
			name: "explicit packageCacheDir overrides dataDir when sandboxDir is empty",
			cfg: Config{
				DataDir:         dataDir,
				PackageCacheDir: customDir,
			},
			want: customDir,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := packageCacheDir(tc.cfg)
			if got != tc.want {
				t.Errorf("packageCacheDir() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestActionsCacheDirResolution(t *testing.T) {
	t.Parallel()

	dataDir := "/var/lib/grain/data"
	sandboxDir := "/var/lib/grain/scratch"
	customDir := "/mnt/storage/custom-actions"

	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{
			name: "fallback to dataDir when sandboxDir and actionsCacheDir are empty",
			cfg: Config{
				DataDir: dataDir,
			},
			want: filepath.Join(dataDir, "cache", "actions"),
		},
		{
			name: "default to sandboxDir when sandboxDir is set",
			cfg: Config{
				DataDir:    dataDir,
				SandboxDir: sandboxDir,
			},
			want: filepath.Join(sandboxDir, "cache", "actions"),
		},
		{
			name: "explicit actionsCacheDir overrides sandboxDir and dataDir",
			cfg: Config{
				DataDir:         dataDir,
				SandboxDir:      sandboxDir,
				ActionsCacheDir: customDir,
			},
			want: customDir,
		},
		{
			name: "explicit actionsCacheDir overrides dataDir when sandboxDir is empty",
			cfg: Config{
				DataDir:         dataDir,
				ActionsCacheDir: customDir,
			},
			want: customDir,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := actionsCacheDir(tc.cfg)
			if got != tc.want {
				t.Errorf("actionsCacheDir() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStartPackageCacheUsesResolvedDir(t *testing.T) {
	t.Parallel()

	t.Run("default to sandboxDir", func(t *testing.T) {
		t.Parallel()
		dataDir := t.TempDir()
		sandboxDir := t.TempDir()

		cfg := Config{
			DataDir:        dataDir,
			SandboxDir:     sandboxDir,
			PackageCacheGB: 1,
		}

		pc, stop, err := StartPackageCache(cfg, NewDirect(""), nil)
		if err != nil {
			t.Fatalf("startPackageCache: %v", err)
		}
		defer stop(context.Background())

		wantDir := filepath.Join(sandboxDir, "cache", "packages")
		if pc.dir != wantDir {
			t.Errorf("pc.dir = %q, want %q", pc.dir, wantDir)
		}
	})

	t.Run("explicit packageCacheDir", func(t *testing.T) {
		t.Parallel()
		dataDir := t.TempDir()
		sandboxDir := t.TempDir()
		customDir := filepath.Join(t.TempDir(), "custom-packages")

		cfg := Config{
			DataDir:         dataDir,
			SandboxDir:      sandboxDir,
			PackageCacheDir: customDir,
			PackageCacheGB:  1,
		}

		pc, stop, err := StartPackageCache(cfg, NewDirect(""), nil)
		if err != nil {
			t.Fatalf("startPackageCache: %v", err)
		}
		defer stop(context.Background())

		if pc.dir != customDir {
			t.Errorf("pc.dir = %q, want %q", pc.dir, customDir)
		}
	})
}

func TestStartActionsCacheUsesResolvedDir(t *testing.T) {
	t.Parallel()

	t.Run("default to sandboxDir", func(t *testing.T) {
		t.Parallel()
		dataDir := t.TempDir()
		sandboxDir := t.TempDir()

		cfg := Config{
			DataDir:        dataDir,
			SandboxDir:     sandboxDir,
			ActionsCacheGB: 1,
		}

		ac, stop, err := startActionsCache(cfg, NewDirect(""))
		if err != nil {
			t.Fatalf("startActionsCache: %v", err)
		}
		defer stop(context.Background())

		wantDir := filepath.Join(sandboxDir, "cache", "actions")
		if ac.cache.Dir() != wantDir {
			t.Errorf("ac.cache.Dir() = %q, want %q", ac.cache.Dir(), wantDir)
		}
	})

	t.Run("explicit actionsCacheDir", func(t *testing.T) {
		t.Parallel()
		dataDir := t.TempDir()
		sandboxDir := t.TempDir()
		customDir := filepath.Join(t.TempDir(), "custom-actions")

		cfg := Config{
			DataDir:         dataDir,
			SandboxDir:      sandboxDir,
			ActionsCacheDir: customDir,
			ActionsCacheGB:  1,
		}

		ac, stop, err := startActionsCache(cfg, NewDirect(""))
		if err != nil {
			t.Fatalf("startActionsCache: %v", err)
		}
		defer stop(context.Background())

		if ac.cache.Dir() != customDir {
			t.Errorf("ac.cache.Dir() = %q, want %q", ac.cache.Dir(), customDir)
		}
	})
}
