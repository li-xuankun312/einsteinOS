package compact

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type SafeFileWriter struct {
	finalPath string
	tmpPath   string
	file      *os.File
	writer    *bufio.Writer
	committed bool
	aborted   bool
	written   int64
}

func NewSafeFileWriter(finalPath string, bufferSize int) (*SafeFileWriter, error) {
	if bufferSize <= 0 {
		bufferSize = 64 * 1024
	}

	dir := filepath.Dir(finalPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create dir: %w", err)
	}

	tmpPath := finalPath + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}

	return &SafeFileWriter{
		finalPath: finalPath,
		tmpPath:   tmpPath,
		file:      f,
		writer:    bufio.NewWriterSize(f, bufferSize),
	}, nil
}

func (s *SafeFileWriter) Write(p []byte) (int, error) {
	if s.committed || s.aborted {
		return 0, fmt.Errorf("writer already finished")
	}
	n, err := s.writer.Write(p)
	s.written += int64(n)
	return n, err
}

func (s *SafeFileWriter) Writer() io.Writer {
	return s
}

func (s *SafeFileWriter) Written() int64 {
	return s.written
}

func (s *SafeFileWriter) Flush() error {
	if s.committed || s.aborted {
		return nil
	}
	return s.writer.Flush()
}

func (s *SafeFileWriter) Commit() error {
	if s.committed {
		return nil
	}
	if s.aborted {
		return fmt.Errorf("cannot commit aborted writer")
	}

	if err := s.writer.Flush(); err != nil {
		s.cleanup()
		return fmt.Errorf("flush: %w", err)
	}

	if err := s.file.Sync(); err != nil {
		s.cleanup()
		return fmt.Errorf("sync: %w", err)
	}

	if err := s.file.Close(); err != nil {
		s.cleanup()
		return fmt.Errorf("close: %w", err)
	}

	if err := os.Rename(s.tmpPath, s.finalPath); err != nil {
		os.Remove(s.tmpPath)
		return fmt.Errorf("rename: %w", err)
	}

	s.committed = true
	return nil
}

func (s *SafeFileWriter) Abort() error {
	if s.committed || s.aborted {
		return nil
	}
	s.aborted = true
	return s.cleanup()
}

func (s *SafeFileWriter) cleanup() error {
	s.file.Close()
	return os.Remove(s.tmpPath)
}

func (s *SafeFileWriter) FinalPath() string {
	return s.finalPath
}

func AtomicWriteFile(path string, data []byte) error {
	w, err := NewSafeFileWriter(path, len(data))
	if err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		w.Abort()
		return err
	}
	return w.Commit()
}

func CopyFileAtomic(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	info, err := srcFile.Stat()
	if err != nil {
		return err
	}

	w, err := NewSafeFileWriter(dst, 256*1024)
	if err != nil {
		return err
	}

	if _, err := io.Copy(w, srcFile); err != nil {
		w.Abort()
		return err
	}

	_ = info
	return w.Commit()
}
