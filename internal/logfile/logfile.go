// Package logfile 은 백그라운드 서비스가 쓰는, 크기 제한이 있는 로그 파일이다.
// 연결마다 한 줄씩 쌓이므로 오래 돌면 무한정 커지지 않도록 돌려 쓴다.
package logfile

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// DefaultMaxSize 를 넘기면 현재 파일을 path+".1" 로 옮기고 새로 시작한다.
const DefaultMaxSize = 10 << 20

type File struct {
	path string
	max  int64

	mu   sync.Mutex
	f    *os.File
	size int64
}

// Open 은 path 에 이어 쓰는 로그 파일을 연다. max <= 0 이면 DefaultMaxSize 다.
func Open(path string, max int64) (*File, error) {
	if max <= 0 {
		max = DefaultMaxSize
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	l := &File{path: path, max: max}
	if err := l.open(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *File) open() error {
	f, err := os.OpenFile(l.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("stat log file: %w", err)
	}
	l.f, l.size = f, info.Size()
	return nil
}

func (l *File) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.size > 0 && l.size+int64(len(p)) > l.max {
		if err := l.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := l.f.Write(p)
	l.size += int64(n)
	return n, err
}

func (l *File) rotate() error {
	if err := l.f.Close(); err != nil {
		return err
	}
	if err := os.Rename(l.path, l.path+".1"); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("rotate log file: %w", err)
	}
	return l.open()
}

func (l *File) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}
