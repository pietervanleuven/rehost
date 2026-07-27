package detect

import (
	"context"
	"io"
	"os"
	"path"
	"path/filepath"
)

// dirFS reads a local directory tree through the same interface as the
// remote host. It backs recipe tests (fixture trees) and could serve a
// local-source mode later. Paths are POSIX and interpreted relative to root.
type dirFS struct{ root string }

// NewDirFS returns an FS rooted at a local directory.
func NewDirFS(root string) FS { return dirFS{root: root} }

// resolve maps a POSIX path from the FS namespace to a local OS path,
// confined to the root.
func (d dirFS) resolve(p string) string {
	clean := path.Clean("/" + p) // strip .. escapes, force absolute
	return filepath.Join(d.root, filepath.FromSlash(clean))
}

func (d dirFS) Exists(_ context.Context, p string) (bool, error) {
	_, err := os.Stat(d.resolve(p))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (d dirFS) IsDir(_ context.Context, p string) (bool, error) {
	info, err := os.Stat(d.resolve(p))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.IsDir(), nil
}

func (d dirFS) ReadFile(_ context.Context, p string) ([]byte, error) {
	f, err := os.Open(d.resolve(p))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxReadBytes))
}

func (d dirFS) List(_ context.Context, dir string) ([]string, error) {
	entries, err := os.ReadDir(d.resolve(dir))
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names, nil
}
