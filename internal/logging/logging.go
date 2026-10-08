// Package logging provides a size-rotated log file and slog setup.
package logging

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

// DefaultMaxSize is the rotation threshold (5 MB).
const DefaultMaxSize = 5 << 20

// RotatingFile is an io.Writer appending to path and rotating it to
// path.1 .. path.N when it exceeds MaxSize.
type RotatingFile struct {
	Path    string
	MaxSize int64
	Keep    int

	mu   sync.Mutex
	f    *os.File
	size int64
}

// OpenRotating opens (creating dirs as needed) a rotating log file.
func OpenRotating(path string, maxSize int64, keep int) (*RotatingFile, error) {
	if maxSize <= 0 {
		maxSize = DefaultMaxSize
	}
	if keep <= 0 {
		keep = 2
	}
	r := &RotatingFile{Path: path, MaxSize: maxSize, Keep: keep}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *RotatingFile) open() error {
	f, err := os.OpenFile(r.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	r.f, r.size = f, st.Size()
	return nil
}

func (r *RotatingFile) rotate() error {
	if r.f != nil {
		r.f.Close()
		r.f = nil
	}
	for i := r.Keep; i >= 1; i-- {
		src := r.Path
		if i > 1 {
			src = r.Path + "." + strconv.Itoa(i-1)
		}
		dst := r.Path + "." + strconv.Itoa(i)
		_ = os.Remove(dst)
		_ = os.Rename(src, dst)
	}
	return r.open()
}

// Write implements io.Writer.
func (r *RotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		if err := r.open(); err != nil {
			return 0, err
		}
	}
	if r.size+int64(len(p)) > r.MaxSize && r.size > 0 {
		if err := r.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

// Close closes the file.
func (r *RotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}

// New builds a text slog logger writing to all non-nil writers.
func New(level slog.Level, ws ...io.Writer) *slog.Logger {
	var out []io.Writer
	for _, w := range ws {
		if w != nil {
			out = append(out, w)
		}
	}
	if len(out) == 0 {
		return slog.New(slog.DiscardHandler)
	}
	return slog.New(slog.NewTextHandler(tolerant(out), &slog.HandlerOptions{Level: level}))
}

// tolerant writes to every writer and never fails because one of them does
// (e.g. an invalid stderr in a GUI-subsystem process).
type tolerant []io.Writer

func (t tolerant) Write(p []byte) (int, error) {
	for _, w := range t {
		_, _ = w.Write(p)
	}
	return len(p), nil
}
