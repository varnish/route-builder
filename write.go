package routebuilder

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// WriteFileAtomic writes content to path atomically by writing to a temp file
// first, then renaming. This prevents partial writes from corrupting the file.
func WriteFileAtomic(path, content string) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".route-builder-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, writeErr := f.WriteString(content)
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil {
		os.Remove(tmp)
		return writeErr
	}
	if syncErr != nil {
		os.Remove(tmp)
		return syncErr
	}
	if closeErr != nil {
		os.Remove(tmp)
		return closeErr
	}
	if err := os.Chmod(tmp, 0644); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// WriteOutput writes content to the given path, or to stdout if path is "-".
func WriteOutput(path, content string, stdout io.Writer) error {
	if path == "-" {
		_, err := fmt.Fprint(stdout, content)
		return err
	}
	return WriteFileAtomic(path, content)
}
