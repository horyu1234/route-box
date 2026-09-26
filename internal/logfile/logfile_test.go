package logfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRotatesPastMaxSizeKeepingOneBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "routebox.log")
	l, err := Open(path, 32)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"first line 0123456789\n", "second line 0123456789\n", "third line 0123456789\n"} {
		if _, err := l.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	cur, _ := os.ReadFile(path)
	old, _ := os.ReadFile(path + ".1")
	if string(cur) != "third line 0123456789\n" || string(old) != "second line 0123456789\n" {
		t.Fatalf("current=%q backup=%q", cur, old)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("log file perm: %v %v", info.Mode(), err)
	}
}

func TestAppendsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routebox.log")
	for _, s := range []string{"a\n", "b\n"} {
		l, err := Open(path, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = l.Write([]byte(s))
		_ = l.Close()
	}
	if b, _ := os.ReadFile(path); !strings.HasPrefix(string(b), "a\nb\n") {
		t.Fatalf("log = %q", b)
	}
}
