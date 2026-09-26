package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	AppDirName = "routebox"
	fileName   = "config.json"
)

// Dir 는 사용자별 RouteBox 디렉터리를 반환한다
// (macOS 는 ~/Library/Application Support/routebox, Linux 는 ~/.config/routebox).
func Dir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user config dir: %w", err)
	}
	return filepath.Join(base, AppDirName), nil
}

func DefaultPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, fileName), nil
}

// Load 는 path 의 config 를 읽고 검증한다. 파일이 없으면 기본값과 함께
// fs.ErrNotExist 에 매칭되는 에러를 반환한다. 파일이 유효하지 않아도 여기서는
// 절대 수정하지 않는다.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Default(), fmt.Errorf("config %s: %w", path, fs.ErrNotExist)
		}
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	cfg := Default()
	cfg.Routes = nil
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	if err := cfg.Normalize(); err != nil {
		return Config{}, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return cfg, nil
}

// Save 는 cfg 를 원자적으로 쓴다: 같은 디렉터리에 temp 파일을 만들고 fsync 한
// 뒤 rename 한다. 리더는 항상 이전 파일 또는 새 파일만 보고, 부분 쓰기는 절대 보지 않는다.
func Save(path string, cfg Config) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("chmod temp config: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	committed = true
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// Backup 은 path 의 파일을 타임스탬프 접미사를 붙여 옆에 복사하고 그 백업
// 경로를 반환한다. 로드에 실패한 config 를 덮어쓰기 전에 사용한다.
func Backup(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read config for backup: %w", err)
	}
	dst := fmt.Sprintf("%s.bak-%s", path, time.Now().Format("20060102-150405"))
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		return "", fmt.Errorf("write config backup: %w", err)
	}
	return dst, nil
}

// Store 는 config 변경을 직렬화하고 각 변경이 보이기 전에 먼저 영속화하므로,
// Update 가 성공한 뒤에는 메모리와 디스크가 절대 어긋나지 않는다.
type Store struct {
	path string

	mu  sync.Mutex
	cfg Config
}

func NewStore(path string, cfg Config) *Store {
	return &Store{path: path, cfg: cfg.Clone()}
}

func (s *Store) Path() string { return s.path }

func (s *Store) Get() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Clone()
}

// Update 는 fn 을 복사본에 적용하고 정규화·저장한 뒤 커밋한다.
// 에러가 나면 저장된 config 는 변경되지 않는다.
func (s *Store) Update(fn func(*Config) error) (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.cfg.Clone()
	if err := fn(&next); err != nil {
		return Config{}, err
	}
	if err := next.Normalize(); err != nil {
		return Config{}, err
	}
	if err := Save(s.path, next); err != nil {
		return Config{}, err
	}
	s.cfg = next
	return next.Clone(), nil
}
