// Package i18n 은 TUI 문구를 영어/한국어로 옮긴다. 영어 문장 자체가 키이며
// 한국어 표에 없는 문구는 영어로 표시된다.
package i18n

import (
	"fmt"
	"os"
	"strings"
)

type Lang string

const (
	EN Lang = "en"
	KO Lang = "ko"
)

// Parse 는 "en"/"ko" 만 받아들인다.
func Parse(s string) (Lang, bool) {
	switch Lang(strings.ToLower(strings.TrimSpace(s))) {
	case EN:
		return EN, true
	case KO:
		return KO, true
	}
	return "", false
}

// Detect 는 실행 인자 > 저장된 설정 > LC_ALL > LC_MESSAGES > LANG 순으로 언어를 고른다.
func Detect(override, saved string) Lang {
	for _, s := range []string{override, saved} {
		if l, ok := Parse(s); ok {
			return l
		}
	}
	for _, env := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := os.Getenv(env); v != "" {
			if strings.HasPrefix(strings.ToLower(v), "ko") {
				return KO
			}
			return EN
		}
	}
	return EN
}

// Next 는 언어 전환 키가 고를 다음 언어다.
func (l Lang) Next() Lang {
	if l == KO {
		return EN
	}
	return KO
}

// Name 은 그 언어로 쓴 언어 이름이다.
func (l Lang) Name() string {
	if l == KO {
		return "한국어"
	}
	return "English"
}

// T 는 key 를 번역하고 args 가 있으면 fmt.Sprintf 로 채운다.
func (l Lang) T(key string, args ...any) string {
	s := key
	if l == KO {
		if v, ok := ko[key]; ok {
			s = v
		}
	}
	if len(args) > 0 {
		return fmt.Sprintf(s, args...)
	}
	return s
}
