package router

import (
	"errors"
	"fmt"
	"strings"
)

var ErrUnknownPreset = errors.New("unknown preset")

// Preset 은 한 서비스를 쓰는 데 필요한 도메인 묶음이다. 서브도메인은 route
// 매칭이 이미 포함하므로 등록 가능한 최상위 이름만 적는다.
type Preset struct {
	Name     string
	Title    string
	Category string
	Domains  []string
}

const (
	CategoryMedia   = "OTT / MEDIA"
	CategoryAI      = "AI"
	CategoryDev     = "DEV"
	CategoryUtility = "UTILITY"
)

var presets = []Preset{
	{Name: "youtube", Title: "YouTube", Category: CategoryMedia, Domains: []string{
		"youtube.com", "youtu.be", "googlevideo.com", "ytimg.com", "youtube-nocookie.com",
		"youtubei.googleapis.com", "ggpht.com", "youtube.googleapis.com", "youtubekids.com", "yt.be",
	}},
	{Name: "twitch", Title: "Twitch", Category: CategoryMedia, Domains: []string{
		"twitch.tv", "ttvnw.net", "jtvnw.net", "twitchcdn.net", "twitchsvc.net",
	}},
	{Name: "chzzk", Title: "CHZZK", Category: CategoryMedia, Domains: []string{
		"chzzk.naver.com",
	}},
	{Name: "netflix", Title: "Netflix", Category: CategoryMedia, Domains: []string{
		"netflix.com", "netflix.net", "nflxvideo.net", "nflximg.net", "nflxext.com", "nflxso.net",
	}},
	{Name: "disneyplus", Title: "Disney+", Category: CategoryMedia, Domains: []string{
		"disneyplus.com", "disney-plus.net", "bamgrid.com", "dssott.com",
	}},
	{Name: "spotify", Title: "Spotify", Category: CategoryMedia, Domains: []string{
		"spotify.com", "scdn.co", "spotifycdn.com",
	}},
	{Name: "chatgpt", Title: "ChatGPT", Category: CategoryAI, Domains: []string{
		"openai.com", "chatgpt.com", "oaistatic.com", "oaiusercontent.com",
	}},
	{Name: "claude", Title: "Claude", Category: CategoryAI, Domains: []string{
		"claude.ai", "anthropic.com",
	}},
	{Name: "gemini", Title: "Gemini", Category: CategoryAI, Domains: []string{
		"gemini.google.com",
	}},
	{Name: "github", Title: "GitHub", Category: CategoryDev, Domains: []string{
		"github.com", "githubusercontent.com", "githubassets.com", "github.io", "ghcr.io",
	}},
	{Name: "docker", Title: "Docker Hub", Category: CategoryDev, Domains: []string{
		"docker.io", "docker.com",
	}},
	{Name: "npm", Title: "npm", Category: CategoryDev, Domains: []string{
		"npmjs.org", "npmjs.com", "yarnpkg.com",
	}},
	{Name: "pypi", Title: "PyPI", Category: CategoryDev, Domains: []string{
		"pypi.org", "pythonhosted.org",
	}},
	{Name: "golang", Title: "Go modules", Category: CategoryDev, Domains: []string{
		"golang.org", "go.dev",
	}},
	{Name: "ipcheck", Title: "IP check", Category: CategoryUtility, Domains: []string{
		"ifconfig.me", "ipinfo.io", "icanhazip.com", "ipify.org",
	}},
}

// Presets 는 카테고리 순서를 유지한 preset 목록을 돌려준다.
func Presets() []Preset {
	return presets
}

// FindPreset 은 이름(대소문자 무시)으로 preset 을 찾는다.
func FindPreset(name string) (Preset, error) {
	n := strings.ToLower(strings.TrimSpace(name))
	for _, p := range presets {
		if p.Name == n {
			return p, nil
		}
	}
	names := make([]string, len(presets))
	for i, p := range presets {
		names[i] = p.Name
	}
	return Preset{}, fmt.Errorf("%w %q (available: %s)", ErrUnknownPreset, name, strings.Join(names, ", "))
}

// Routes 는 preset 의 도메인을 via 로 보내는 route 들로 만든다.
func (p Preset) Routes(via string) ([]Route, error) {
	out := make([]Route, 0, len(p.Domains))
	for _, d := range p.Domains {
		r, err := NewRoute(d, via)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}
