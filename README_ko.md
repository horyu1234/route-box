# RouteBox

[English](README.md) | **한국어**

**Selective Tunnel Router** — 고른 도메인과 주소를 고른 터널로 보내고, 나머지는 직접 연결하는 로컬 HTTP 프록시.

브라우저 프록시를 `127.0.0.1:8080`으로 한 번만 맞춰 두면, 그다음부터는 터미널 UI에서 어떤 서비스를 어느 출구로 보낼지 정합니다. 한 지역의 SSH 서버, 다른 지역의 SSH 서버, 이미 띄워 둔 SOCKS5 서버, 또는 직접 연결 중에서 고르면 됩니다. 바꾼 내용은 다음 연결부터 바로 적용되고 재시작, PAC 파일, 브라우저 확장이 필요 없습니다.

```
 RouteBox                                                                              ● 연결됨
 선택적 터널 라우터                                                  대기 주소 127.0.0.1:8080
╭──────────────────────────────────────────╮╭──────────────────────────────────────────────────╮
│ 라우트 4                                 ││ 실시간 연결 실시간                               │
│ ▌● example.com                   → seoul ││ 16:30:01 seoul    www.example.com:443   ● open   │
│  ● example.org                   → tokyo ││ 16:30:01 tokyo    cdn.example.org:443   ✓ 2.0s   │
│  ● 203.0.113.10                    → lab ││ 16:30:02 DIRECT   intranet.example.com  ✓ 1.2s   │
│  ○ intranet.example.com           DIRECT ││ 16:30:03 lab      203.0.113.10:443      ● open   │
│  + 라우트 추가                           ││                                                  │
╰──────────────────────────────────────────╯╰──────────────────────────────────────────────────╯
╭──────────────────────────────────────────────────────────────────────────────────────────────╮
│ HTTP 127.0.0.1:8080  ·  가동 1h02m  ·  활성 3  전체 120  프록시 80  직접 38  실패 2          │
│ ● seoul        ssh proxy-seoul      SOCKS 127.0.0.1:1080   연결됨 · PID 4121      라우트 1개 │
│ ● tokyo        ssh proxy-tokyo      SOCKS 127.0.0.1:1081   연결됨 · PID 4122      라우트 1개 │
│ ● lab          외부 SOCKS           SOCKS 127.0.0.1:9050   응답함                 라우트 1개 │
╰──────────────────────────────────────────────────────────────────────────────────────────────╯
 [a] 추가  [v] 경로  [e] 편집  [d] 삭제  [s] 업스트림  [p] preset  [r] 재시작  [?] 도움말  [q] 종료
```

## 목차

1. [하는 일](#1-하는-일)
2. [동작 구조](#2-동작-구조)
3. [왜 쓰나](#3-왜-쓰나)
4. [설치](#4-설치)
5. [빌드](#5-빌드)
6. [Firefox 설정](#6-firefox-설정)
7. [업스트림: managed SSH](#7-업스트림-managed-ssh)
8. [업스트림: external SOCKS](#8-업스트림-external-socks)
9. [도메인·주소 라우팅](#9-도메인주소-라우팅)
10. [Preset](#10-preset)
11. [TUI 키](#11-tui-키)
12. [CLI](#12-cli)
13. [설정](#13-설정)
14. [문제 해결](#14-문제-해결)
15. [보안](#15-보안)
16. [아키텍처](#16-아키텍처)
17. [라이선스](#라이선스)

## 1. 하는 일

- 로컬에 **HTTP CONNECT 프록시**를 엽니다(기본 `127.0.0.1:8080`).
- **업스트림**을 원하는 만큼 등록합니다. RouteBox가 직접 띄우고 감시하는 SSH 터널(`ssh -N -D`)이거나, 이미 실행 중인 SOCKS5 서버입니다.
- **라우트**를 등록합니다. 도메인(과 그 모든 서브도메인) 또는 정확한 IPv4/IPv6 주소를 특정 업스트림으로(**via**) 보내거나 `direct`로 보냅니다.
- 어떤 라우트에도 해당하지 않으면 기본으로 **직접 연결**하고, 원하면 고른 업스트림으로 보냅니다.
- 변경은 **즉시** 반영됩니다. 기존 연결은 그대로 두고 새 연결부터 새 규칙을 탑니다.
- TLS를 절대 복호화하지 않습니다. 인증서 생성·MITM·SNI 변경이 없고, CONNECT 터널은 바이트를 그대로 중계합니다.

VPN, TUN/TAP, 패킷 캡처, 브라우저 확장은 만들지 않습니다. 구조는 **HTTP CONNECT 프록시 + SOCKS5 + SSH dynamic forwarding**으로 고정입니다.

## 2. 동작 구조

```
브라우저
  │  HTTP CONNECT www.example.com:443
  ▼
RouteBox 127.0.0.1:8080 ── 라우트 조회: example.com → via seoul
  │
  ├── via seoul ──► SOCKS5 127.0.0.1:1080 ── ssh -N -D … proxy-seoul ──► 원격 DNS + 접속 ──► 사이트
  ├── via tokyo ──► SOCKS5 127.0.0.1:1081 ── ssh -N -D … proxy-tokyo ──► 원격 DNS + 접속 ──► 사이트
  ├── via lab   ──► SOCKS5 127.0.0.1:9050 (직접 띄운 서버) ────────────────────────────────► 사이트
  └── 라우트 없음 / direct ──► OS resolver + 직접 TCP ─────────────────────────────────────► 사이트
```

- **매칭.** `example.com` 라우트는 `example.com`, `www.example.com`, `a.b.example.com`을 포함하고, `notexample.com`이나 `example.com.attacker.net`은 포함하지 않습니다. 호스트 레이블을 하나씩 떼어 가며 찾으므로 **가장 구체적인 라우트가 이깁니다**. `example.com → seoul`과 `intranet.example.com → direct`를 함께 두면 인트라넷만 직접 연결됩니다. **와일드카드** 라우트 `*.example.com`은 서브도메인(`www.example.com`, `a.b.example.com`)만 포함하고 `example.com` 자체는 포함하지 않습니다. `example.com` 라우트와 함께 두면 와일드카드가 더 구체적이므로, `example.com → direct`와 `*.example.com → seoul`로 도메인 자체와 서브도메인을 나눠 보낼 수 있습니다. 와일드카드는 맨 앞에만 쓸 수 있습니다(`api.*.example.com`은 거부). IP 라우트는 그 주소와 정확히 같을 때만 적용됩니다.
- **DNS는 원격에서.** 업스트림으로 가는 연결은 로컬 resolver를 한 번도 쓰지 않습니다. hostname을 SOCKS 서버에 도메인 타입(ATYP `0x03`)으로 넘기므로 이름 해석은 터널 반대편에서 일어납니다. 로컬 DNS가 막거나 조작해도 라우팅한 도메인은 영향을 받지 않습니다. 테스트로 고정되어 있습니다([아키텍처](#16-아키텍처) 참고).
- **실패 시 차단(fail closed).** 라우트의 업스트림이 죽었거나 삭제됐으면 `502 Bad Gateway`를 돌려줍니다. 다른 업스트림이나 직접 연결로 몰래 새지 않습니다. **그 외 전부**를 업스트림으로 보낼 때 매칭되지 않은 트래픽도 마찬가지입니다.

## 3. 왜 쓰나

- 서비스마다 출구가 달라야 할 때: 어떤 서비스는 이 지역, 다른 서비스는 저 지역, 내부 호스트는 실험실 네트워크, 나머지는 그대로.
- 로컬 DNS가 특정 이름을 막거나 바꿔서, 그 이름만 원격에서 해석되게 하고 싶을 때.
- PAC 파일을 고치고 브라우저를 새로 고치는 대신 터미널에서 바로 라우팅을 바꾸고 싶을 때.
- 가진 게 SSH 접속뿐일 때. `ssh -D`면 충분하고 서버에 따로 설치할 것이 없습니다.

## 4. 설치

Go가 있으면(필요한 버전은 `go.mod` 참고) 최신 릴리스 태그를 지정해 설치합니다.

```sh
go install github.com/horyu1234/route-box/cmd/routebox@v0.3.0
```

태그는 항상 직접 지정하세요. `@latest`는 Go 모듈 프록시를 거치는데, 프록시는 새 버전을 낸 뒤에도 한동안 예전 버전을 줄 수 있습니다. 새 릴리스는 [태그 목록](https://github.com/horyu1234/route-box/tags)에서 확인하고, 설치된 버전은 `routebox --version`으로 봅니다.

또는 소스에서 빌드합니다([빌드](#5-빌드)). 결과물은 정적 단일 바이너리(`CGO_ENABLED=0`)라 `PATH` 어디에 복사해도 됩니다.

macOS Gatekeeper가 내려받은 바이너리를 막으면 `xattr -d com.apple.quarantine routebox`.

### 백그라운드 실행

Firefox가 RouteBox를 가리키고 있으면, RouteBox가 꺼져 있는 동안에는 **모든** 사이트가 "프록시 서버가 연결을 거부했습니다"로 막힙니다. 사용자 서비스로 등록하면 로그인할 때 뜨고, 비정상 종료되면 다시 뜹니다.

```sh
routebox service install     # macOS: launchd LaunchAgent · Linux: systemd --user unit
```

그 뒤로 `routebox`를 실행하면 TUI가 백그라운드 인스턴스의 **관리 패널**로 붙습니다. 모든 변경은 제어 소켓으로 그 인스턴스에 전달되고, `q`는 패널만 닫으며 프록시와 ssh 터널은 계속 돕니다. CLI 명령(`route`, `upstream`, `status` …)도 같은 방식으로 동작합니다.

```sh
routebox service status      # 설치 여부, 실행 여부, 로그 위치
routebox service restart     # 바이너리를 교체한 뒤 등
routebox service stop        # 다음 로그인까지 (또는 `routebox service start`)
routebox service uninstall   # 멈추고 로그인 항목에서 제거
```

- 서비스는 `install`을 실행한 셸의 설정 경로와 `PATH`로 `routebox --no-tui --config <절대 경로>`를 실행합니다. `--config`나 `ROUTEBOX_CONFIG`를 쓴다면 `install`과, 붙을 때의 `routebox`에 같은 값을 넘기세요. 바이너리나 설정을 옮겼으면 `install`을 다시 실행합니다.
- 로그: macOS는 `~/Library/Logs/RouteBox/routebox.log` (10 MB에서 교체, `.1` 백업 하나 유지), Linux는 journal(`journalctl --user -u routebox`)을 씁니다.
- 설치할 때 터미널에서 RouteBox가 이미 돌고 있으면, 서비스는 계속 재시도하다가, 그 인스턴스를 종료하면 10초 안팎에 이어받습니다. macOS에서는 재시도마다 `~/Library/Logs/RouteBox/stderr.log`에 `another RouteBox instance is already running` 줄이 쌓이므로, 서비스와 터미널 인스턴스를 함께 켜 두지 마세요.
- ssh가 터미널 없이 실행되므로 passphrase를 묻거나 호스트 키를 확인받을 수 없습니다. agent에 들어 있는 키를 쓰고(macOS: `UseKeychain yes` + `AddKeysToAgent yes`, 또는 1Password 같은 `IdentityAgent`), 서버마다 호스트 키를 한 번 신뢰해 두세요(업스트림 관리 창의 `t` 또는 `routebox ssh trust <업스트림>`, [호스트 키](#호스트-키) 참고). Linux의 `systemd --user` 서비스는 셸의 `SSH_AUTH_SOCK`을 물려받지 않습니다. [문제 해결](#14-문제-해결)을 보세요.
- 직접 쓰는 프로세스 관리자나 로그인 항목으로 띄워도 됩니다. `routebox --no-tui` (필요하면 `--log-file`도)를 실행하면 됩니다.

### 업데이트

1. 실행 중인 인스턴스를 종료합니다. TUI는 `q`, `--no-tui`는 `Ctrl+C`. `routebox status`가 `RouteBox   not running`을 출력하면 됩니다. [백그라운드 서비스](#백그라운드-실행)를 쓰면 이 단계는 건너뛰어도 됩니다.
2. 필요하면 `config.json`을 백업합니다([설정](#13-설정)).
3. 설치했던 방법 그대로 바이너리를 교체합니다. [태그 목록](https://github.com/horyu1234/route-box/tags)에서 새 태그를 확인해 지정합니다.

   ```sh
   go install github.com/horyu1234/route-box/cmd/routebox@v0.3.0
   # 또는 소스 체크아웃에서:
   git pull && make build   # 이후 bin/routebox로 기존 바이너리를 덮어씁니다
   ```

4. `routebox`를 다시 실행합니다. 백그라운드 서비스라면 `routebox service restart`를 실행합니다(재시작 전까지는 예전 바이너리가 계속 돕니다). 라우트와 업스트림은 설정 파일에 그대로 남아 있고, 예전 버전의 설정은 읽을 때 새 형식으로 옮겨집니다.

`routebox --version`은 설치된 버전을 보여 줍니다. `go install`이면 모듈 버전(예: `v0.3.0`), `make build`면 `git describe` 값입니다.

## 5. 빌드

```sh
make build   # bin/routebox
make run     # 빌드 후 실행 (ARGS="--no-tui"로 인자 전달)
make test    # go test ./...
make race    # go test -race ./...
make lint    # gofmt 검사 + go vet (+ golangci-lint가 있으면 실행)
make cross   # dist/: darwin/arm64, darwin/amd64, linux/amd64, linux/arm64
make clean
```

모든 빌드 타깃은 `CGO_ENABLED=0`입니다. `make race`만 race detector 때문에 cgo를 켭니다.

## 6. Firefox 설정

1. **설정(Settings)** → **일반(General)** → 아래쪽 **네트워크 설정(Network Settings)** → **설정…(Settings…)**
2. **수동 프록시 설정(Manual proxy configuration)** 선택
3. **HTTP 프록시** `127.0.0.1`, **포트** `8080`
4. HTTPS도 같은 프록시를 쓰게 합니다. 버전에 따라 **"Also use this proxy for HTTPS"** 체크박스이거나, 예전 버전은 **"Use this proxy server for all protocols"**입니다. 대신 HTTPS 프록시 칸이 따로 있으면 거기에도 `127.0.0.1` / `8080`을 넣습니다.
5. **SOCKS 호스트**는 비워 둡니다. SOCKS는 RouteBox가 처리합니다.
6. **확인**

Firefox는 HTTPS를 `CONNECT host:443`으로 보내므로 RouteBox는 호스트 이름만 보고 라우팅합니다. 메뉴 문구는 Firefox 버전과 언어에 따라 다를 수 있습니다.

> RouteBox가 실행 중이 아니면 Firefox는 이 프록시로 어떤 페이지도 열지 못합니다. [백그라운드 서비스](#백그라운드-실행)를 설치해 늘 떠 있게 하세요.

> Firefox에만 설정하면 다른 앱은 RouteBox를 거치지 않습니다. 시스템 프록시를 따르는 앱 전체에 적용하려면 OS 프록시 설정에 같은 주소를 넣습니다(macOS: 시스템 설정 → 네트워크 → 세부사항 → 프록시 → 웹 프록시(HTTP), 보안 웹 프록시(HTTPS)).

## 7. 업스트림: managed SSH

managed 업스트림마다 RouteBox가 `ssh -N -D` 프로세스를 하나씩 띄우고 감시합니다.

처음 실행하면 TUI가 첫 업스트림을 물어봅니다. 그다음부터는 **`s`**로 업스트림 관리 창을 엽니다(`a` 추가, `e` 편집, `d` 삭제, `r` 재시작, `t` 호스트 키 확인).

| 필드 | 의미 |
|---|---|
| 종류 | `managed` |
| 이름 | 라우트가 가리키는 이름(`seoul`, `work`, `lab` …). 비우면 호스트에서 자동으로 짓습니다. `direct`는 예약어입니다. |
| SSH 호스트 | `~/.ssh/config`의 `Host` 별칭 또는 호스트명 |
| SSH 사용자 / SSH 포트 | 선택. 비우면 `~/.ssh/config` 값을 따릅니다 |
| 키 파일 | 선택. 비우면 ssh가 정합니다: `~/.ssh/config`의 `IdentityFile`, agent의 키, 기본 `~/.ssh/id_*` 순입니다. 입력 칸의 회색 글자는 예시일 뿐입니다. **경로만** 저장하고 키 내용은 저장하지 않습니다 |
| 자동 재연결 | ssh가 종료되면 다시 시작합니다. 연결이 끊긴 경우는 지수 백오프(1초 → 30초)로, 로그인 거절·신뢰하지 않은 호스트 키·서버의 접속 거부는 첫 재시도부터 30초 간격으로 합니다. 이런 실패는 빨리 다시 시도해도 풀리지 않고, 로그인 실패가 반복되면 sshd가 IP를 막기 때문입니다 |
| 로컬 SOCKS | `ssh -D`가 열 주소. 업스트림마다 달라야 하며, 비어 있는 `127.0.0.1:10xx`를 자동으로 제안합니다 |

실제로 실행하는 명령:

```sh
ssh -N -D 127.0.0.1:1080 \
    -o ExitOnForwardFailure=yes -o ServerAliveInterval=30 -o ServerAliveCountMax=3 \
    -o BatchMode=yes -o ConnectTimeout=10 \
    [-p PORT] [-l USER] [-i IDENTITY] -- HOST
```

- 비어 있는 필드는 인자로 넘기지 않으므로 `~/.ssh/config`의 `User`, `Port`, `IdentityFile`, `ProxyJump` 등이 그대로 적용됩니다.
- **공개키 인증 전용**입니다. `BatchMode=yes`라서 비밀번호나 호스트 키 확인을 묻지 않고 실패합니다. ssh가 쓸 수 있는 키는 모두 됩니다. **키 파일** 필드(`-i`), `~/.ssh/config`의 `IdentityFile`, 기본 `~/.ssh/id_*` 키, 그리고 agent에 올린 키(`SSH_AUTH_SOCK`의 `ssh-agent`, 또는 `~/.ssh/config`의 `IdentityAgent`가 가리키는 1Password·Secretive 등)입니다. 비밀번호가 걸린 키는 ssh가 비밀번호를 물을 수 없으므로 먼저 agent에 올려 두세요(`ssh-add`).
- ssh는 인증이 끝난 뒤에야 `-D` 포트를 엽니다. 그래서 SOCKS 인사가 성공해야 연결됨으로 봅니다.
- ssh의 stderr는 TUI 토스트, 업스트림 관리 창, `routebox ssh status`에 표시됩니다.
- 종료할 때 모든 ssh 자식에 SIGTERM(3초 뒤 SIGKILL)을 보내고 회수합니다. 좀비가 남지 않습니다.

### 호스트 키

ssh가 터미널 없이 실행되므로 처음 보는 서버에 대해 "Are you sure you want to continue connecting?"을 물을 수 없고, 터널은 `Host key verification failed`로 실패합니다. 이때 RouteBox가 토스트로 알려 주고, 업스트림 관리 창(`s`)에서 **`t`**로 호스트 키를 확인할 수 있습니다.

1. RouteBox가 ssh로(`~/.ssh/config`의 `Port`, `HostKeyAlias`, `HashKnownHosts` 등을 그대로 따라) 인증 없이 접속해, 서버가 보낸 키 종류와 `SHA256:` 지문을 보여 줍니다.
2. 서버에서 확인한 지문과 비교합니다. 예: `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub`.
3. `y`를 누르면 ssh가 직접 쓴 그 키 줄을 그대로 `known_hosts`(첫 번째 `UserKnownHostsFile`)에 추가하고 다시 연결합니다. `n`은 아무것도 저장하지 않습니다.

명령줄에서는 `routebox ssh trust <업스트림>`으로 같은 일을 합니다. 저장된 키와 **달라진** 키는 중간자 공격의 모습과 같으므로 신뢰를 제안하지 않고, 새 키를 확인한 뒤 `ssh-keygen -R <호스트>`로 예전 키를 지우라고 안내합니다. `ProxyJump`를 쓰면 점프 호스트의 키는 미리 신뢰돼 있어야 합니다. 터미널에서 `ssh <호스트>`로 한 번 접속해도 됩니다.

`~/.ssh/config` 예:

```
Host proxy-seoul
    HostName 203.0.113.10
    User ubuntu
    IdentityFile ~/.ssh/id_ed25519

Host proxy-tokyo
    HostName 198.51.100.20
    User ubuntu
```

## 8. 업스트림: external SOCKS

이미 실행 중인 SOCKS5 서버를 씁니다. 직접 띄운 `ssh -D` 든, 인증 없는 SOCKS5 서버든 됩니다.

```sh
ssh -N -D 127.0.0.1:9050 user@lab-gateway
routebox upstream add lab --external --socks 127.0.0.1:9050
```

external 업스트림에서는 RouteBox가 ssh를 건드리지 않고, 5초마다 SOCKS 서버가 응답하는지만 확인합니다.

명령줄의 `--socks`는 이번 실행에서만 **RouteBox가 시작할 때 첫 번째였던** 업스트림의 SOCKS 주소를 바꿉니다(저장하지 않음). 덮어쓰기는 그 업스트림에 붙어 있어서, 그 업스트림을 지워도 다른 업스트림으로 옮겨 가지 않고, 편집하면 덮어쓰기가 풀립니다.

## 9. 도메인·주소 라우팅

TUI에서:

- **`a`** — 라우트 추가. 도메인이나 IP 주소를 입력하거나 URL을 통째로 붙여 넣습니다. `https://WWW.Example.com:443/watch?v=1`은 `www.example.com`으로 저장됩니다(scheme·path·query·port 제거, 소문자, 끝의 `.` 제거). `*.`로 시작하면 서브도메인만 매칭합니다. **경로(Via)**는 ←/→ 로 고릅니다.
- **`v`**(또는 스페이스) — 선택한 라우트를 다음 업스트림으로 보냅니다. 마지막 업스트림 다음은 `direct`입니다. 바로 적용됩니다.
- **`e`** — 도메인과 경로 편집. **`d`** — 삭제.
- **그 외 전부** — 라우트 목록 아래 행으로, 어떤 라우트에도 매칭되지 않은 트래픽이 나가는 곳입니다. 기본값은 `DIRECT`이고, `v`(또는 `enter`)로 다음 업스트림으로 보내며 마지막 다음은 다시 `direct`입니다. 여기서 쓰는 업스트림은 이걸 바꾸기 전까지 삭제할 수 없습니다.
- 라우트마다 나가는 곳(`→ seoul`, `DIRECT`)이 그 업스트림의 상태 색으로 표시되고, 그 옆에 hit 수, 즉 RouteBox가 시작된 뒤 그 라우트에 매칭된 연결·요청 수가 표시됩니다(`12k` = 12,000 이상). 서브도메인 요청은 매칭된 라우트로 셉니다. 재시작하거나 라우트를 삭제하거나 도메인을 편집하면 0으로 돌아가지만, `v`로 옮기면 유지됩니다.

CLI로:

```sh
routebox route add example.com --via seoul
routebox route add 203.0.113.10 --via lab
routebox route add intranet.example.com --via direct   # example.com 안의 예외
routebox route add '*.example.org' --via seoul         # 서브도메인만, example.org 자체는 제외
routebox route via example.com tokyo                   # 라우트 옮기기
routebox route default seoul                           # 매칭되지 않은 트래픽을 seoul로 (`direct`로 되돌리기)
routebox route list                                    # RouteBox가 실행 중이면 HITS 열 표시
```

업스트림이 하나도 없을 때 추가한 라우트는 처음 만드는 업스트림에 붙습니다. 그 뒤로 라우트는 항상 업스트림 이름을 명시하므로, 업스트림 순서를 바꿔도 다른 출구로 옮겨 가지 않습니다. 라우트가 쓰고 있는 업스트림은 삭제할 수 없습니다. 먼저 그 라우트들을 옮기세요(`v`).

## 10. Preset

preset은 관련 도메인 묶음을 한 번에, 고른 업스트림으로 추가합니다.

```sh
routebox preset list
routebox preset add <이름> --via seoul
routebox --preset <이름>        # 시작하면서 추가
# TUI: p → preset 선택 → 업스트림 선택
```

| 카테고리 | preset |
|---|---|
| OTT / 미디어 | 스트리밍·음악 서비스 |
| AI | AI 어시스턴트 |
| 개발 | 코드 호스팅·패키지 레지스트리 |
| 유틸리티 | `ipcheck` — "내 IP 확인" 서비스, 터널이 제대로 걸렸는지 볼 때 편합니다 |

`routebox preset list`는 preset과 도메인을 모두 보여 줍니다. preset에는 서비스가 쓰는 것으로 알려진 도메인을 넣었지만, 서비스는 CDN을 자주 바꿉니다. 여전히 직접 연결되는 부분이 있으면 서비스를 쓰면서 **실시간 연결** 패널의 `DIRECT` 항목을 보고, 빠진 도메인을 `a`로 추가하세요.

## 11. TUI 키

| 키 | 동작 |
|---|---|
| `↑`/`k`, `↓`/`j` | 선택 이동 (로그에 포커스가 있으면 스크롤) |
| `tab` | 라우트 ↔ 실시간 연결 포커스 전환 |
| `a` | 라우트 추가 |
| `v` / 스페이스 | 선택한 라우트를 다음 업스트림으로 (**그 외 전부**에서는 매칭되지 않은 트래픽이 나갈 곳을 바꿈) |
| `e` / `enter` | 선택한 라우트 편집 (`+ 라우트 추가` 행에서는 추가) |
| `d` | 선택한 라우트 삭제 (`y`로 확인) |
| `p` | preset 추가 |
| `s` | 업스트림 관리 (`a` 추가, `e` 편집, `d` 삭제, `r` 재시작, `t` 호스트 키 확인) |
| `r` | 모든 업스트림 재시작 |
| `l` | 로그 표시/숨김 (좁은 화면에서는 라우트 ↔ 로그 전환) |
| `c` | 연결 기록 지우기 |
| `o` | 연결 기록 끄기/켜기, 설정에 저장 |
| `L` | 언어 전환 (English ↔ 한국어), 설정에 저장 |
| `g` / `G` | 맨 위 / 맨 아래 |
| `?` | 도움말 |
| `esc` | 창 닫기 |
| `q`, `ctrl+c` | 종료 (ssh 터널 정리, 한 번 더 누르면 즉시 종료). [백그라운드 인스턴스](#백그라운드-실행)에 붙어 있을 때는 패널만 닫음 |

레이아웃은 터미널 크기에 맞춰 바뀝니다. 96열 이상이면 패널을 나란히, 그보다 좁으면 라우트를 먼저 보여 주고 `l`로 로그와 바꿉니다. 22행 미만이면 하단이 한 줄 상태 표시로 줄고, 50×14보다 작으면 "터미널이 너무 작습니다"를 표시합니다.

**언어.** TUI는 영어와 한국어를 지원합니다. `--lang` → 저장된 `language` 설정 → `LC_ALL` / `LC_MESSAGES` / `LANG`(`ko_*`면 한국어) 순서로 정합니다. `L`로 바꾸면 저장됩니다.

## 12. CLI

```sh
routebox                                   # TUI (RouteBox가 이미 실행 중이면 관리 패널로 붙음)
routebox --no-tui                          # 헤드리스, 이벤트를 stderr에 기록
routebox --no-tui --log-file PATH          # 헤드리스, 10 MB에서 교체되는 파일에 기록
routebox --listen 127.0.0.1:8080           # 이번 실행에만 (저장 안 함)
routebox --socks 127.0.0.1:1081            # 첫 업스트림의 SOCKS, 이번 실행에만
routebox --lang ko                         # TUI 언어, 이번 실행에만

routebox upstream add seoul --host proxy-seoul [--user U --port N --identity PATH --socks ADDR --no-reconnect]
routebox upstream add lab --external --socks 127.0.0.1:9050
routebox upstream list                     # --json
routebox upstream remove lab

routebox route add example.com --via seoul
routebox route via example.com tokyo
routebox route default [업스트림|direct]    # 매칭되지 않은 트래픽이 나갈 곳
routebox route remove example.com
routebox route list                        # --json

routebox preset list
routebox preset add <이름> --via seoul

routebox status                            # --json, 실행 중이 아니면 종료 코드 1
routebox log off|on                        # 연결 기록 중지 / 재개 (저장됨)
routebox log clear                         # 지금까지 기록한 연결 지우기
routebox ssh status [업스트림]             # 상태 + 최근 ssh stderr
routebox ssh restart [업스트림]
routebox ssh trust <업스트림>              # 호스트 키 지문을 보여 주고, 확인하면 저장

routebox service install|uninstall         # 로그인할 때 실행 (launchd / systemd --user)
routebox service start|stop|restart|status
```

CLI와 TUI는 같은 코어(`internal/core`)를 씁니다. RouteBox가 실행 중이면 CLI는 제어 소켓으로 실행 중인 인스턴스에 요청하므로 즉시 반영됩니다. 실행 중이 아니면 같은 코드로 설정 파일만 고치고 다음 실행부터 적용됩니다. 같은 설정 디렉터리로 두 번째 인스턴스를 띄우면 거부하므로 ssh가 중복 실행되지 않습니다. 대신 `routebox`(TUI)를 실행하면 실행 중인 인스턴스에 붙고, `--listen`/`--socks`는 시작할 때만 적용되므로 거부합니다.

## 13. 설정

`os.UserConfigDir()` 기준:

| OS | 경로 |
|---|---|
| macOS | `~/Library/Application Support/routebox/config.json` |
| Linux | `~/.config/routebox/config.json` (`$XDG_CONFIG_HOME` 존중) |

`--config <경로>` 또는 `ROUTEBOX_CONFIG`로 바꿀 수 있습니다.

```json
{
  "listen": "127.0.0.1:8080",
  "language": "ko",
  "upstreams": [
    { "name": "seoul", "mode": "managed", "host": "proxy-seoul", "socks": "127.0.0.1:1080", "reconnect": true },
    { "name": "tokyo", "mode": "managed", "host": "proxy-tokyo", "user": "me", "port": 2222,
      "identity_file": "~/.ssh/id_ed25519", "socks": "127.0.0.1:1081", "reconnect": true },
    { "name": "lab", "mode": "external", "socks": "127.0.0.1:9050", "reconnect": false }
  ],
  "routes": [
    { "domain": "example.com", "mode": "proxy", "upstream": "seoul" },
    { "domain": "example.org", "mode": "proxy", "upstream": "tokyo" },
    { "domain": "203.0.113.10", "mode": "proxy", "upstream": "lab" },
    { "domain": "intranet.example.com", "mode": "direct" }
  ],
  "fallback": "seoul",
  "connection_log_off": false
}
```

- `fallback`은 어떤 라우트에도 매칭되지 않은 트래픽이 나갈 업스트림입니다. 생략하거나 `"direct"`로 두면 직접 연결합니다.
- `connection_log_off: true`면 연결을 기록하지 않습니다(TUI의 `o`, `routebox log off`). 트래픽 통계와 hit 수는 계속 셉니다.
- `user`/`port`/`identity_file`를 생략하면 `~/.ssh/config`를 따릅니다. `"port": 22`를 적으면 `-p 22`를 넘겨 그 값을 덮어씁니다.
- 변경은 즉시 저장되며 **atomic write**입니다. 같은 디렉터리의 임시 파일에 쓰고 fsync 한 뒤 rename 합니다. 파일은 `0600`, 디렉터리는 `0700`입니다.
- 설정을 읽지 못하면 RouteBox는 **원본을 지우거나 덮어쓰지 않습니다.** TUI는 오류를 보여 주고, 안전한 기본값으로 계속하기를 고르면 먼저 `config.json.bak-YYYYMMDD-HHMMSS`로 백업합니다. `--no-tui`와 CLI는 오류를 출력하고 종료합니다.
- 예전 버전의 단일 `socks`/`ssh` 형식 설정은 읽을 때 `default`라는 업스트림 하나로 옮겨집니다.
- 제어 소켓은 같은 디렉터리의 `routebox.sock`(`0600`)입니다. 경로가 유닉스 소켓 길이 제한을 넘으면 임시 디렉터리의 `routebox-<uid>.sock`을 씁니다.

## 14. 문제 해결

| 증상 | 원인과 조치 |
|---|---|
| Firefox: 모든 사이트에서 "프록시 서버가 연결을 거부했습니다" | RouteBox가 실행 중이 아닙니다. 실행하거나 [백그라운드 서비스](#백그라운드-실행)를 설치하세요. |
| TUI에서는 되는데 서비스로는 터널이 실패 | 서비스에는 터미널이 없어서 agent에 없는 passphrase 키나 처음 보는 호스트 키에 답할 수 없습니다. 호스트 키는 `t` / `routebox ssh trust`로 신뢰하세요. [백그라운드 실행](#백그라운드-실행)을 보고 서비스 로그를 확인하세요. |
| Linux 서비스에서 `Permission denied (publickey)` | `systemd --user`는 셸의 `SSH_AUTH_SOCK`을 모릅니다. `~/.ssh/config`에 `IdentityAgent`를 지정하거나, 로그인 세션에서 `systemctl --user import-environment SSH_AUTH_SOCK`을 실행한 뒤 `routebox service restart`. |
| `Host key verification failed` | 서버의 호스트 키가 아직 `known_hosts`에 없습니다. `s`에서 업스트림을 골라 `t`를 누르고, 지문을 비교한 뒤 `y`를 누르세요(또는 `routebox ssh trust <업스트림>`). [호스트 키](#호스트-키) 참고. |
| `Permission denied (publickey)` | 키가 서버에 등록되지 않았거나, 비밀번호 걸린 키가 agent에 없습니다. `ssh-add ~/.ssh/id_ed25519` 후 `r`. |
| `Connection closed by <ip> port <n>` (서버 로그: `drop connection … penalty`) | 로그인 실패가 쌓여 sshd(OpenSSH 9.8+의 `PerSourcePenalties` 또는 fail2ban)가 잠시 이 IP를 거부하고 있습니다. 몇 분 안에 저절로 풀리니 로그인 문제부터 해결하세요. 여러 키를 내밀고 있다면 이 호스트에 `IdentitiesOnly yes`와 맞는 `IdentityFile`을 지정하세요. 직접 운영하는 서버라면 `PerSourcePenaltyExemptList`로 고정 IP를 예외 처리할 수 있습니다. |
| 키를 `ssh-agent`에 올렸는데도 `Permission denied (publickey)` | `~/.ssh/config`에 `IdentityAgent`(예: 1Password)가 있으면 ssh는 `SSH_AUTH_SOCK` 대신 그 agent에 묻습니다. 그 agent에 키를 넣거나 이 호스트에 맞는 `IdentityAgent`를 지정하세요. 어떤 agent와 키를 시도하는지는 `ssh -v <호스트>`로 볼 수 있습니다. |
| `Could not resolve hostname` | 호스트 오타나 `~/.ssh/config` 누락입니다. `ssh -G <호스트>`로 확인하세요. |
| `SOCKS port already in use` | 다른 `ssh -D`가 이미 그 포트를 씁니다. 그것을 external 업스트림으로 추가하거나, 이 업스트림의 로컬 SOCKS 주소를 바꾸세요. |
| `SOCKS address already used by another upstream` | 업스트림마다 SOCKS 주소가 달라야 합니다. |
| `listen 127.0.0.1:8080: address already in use` | 다른 앱이 8080을 씁니다. `--listen 127.0.0.1:8081`이나 설정의 `listen`을 바꾸고 Firefox도 맞추세요. |
| `another RouteBox instance is already running` | `routebox status`로 확인하거나 그 인스턴스를 종료하세요. |
| `upstream is still in use` | 삭제하기 전에 그 라우트들을 다른 업스트림으로 옮기세요(`v` 또는 `routebox route via`). 매칭되지 않은 트래픽이 그 업스트림으로 나가고 있다면 그것부터 바꾸세요(**그 외 전부** 행에서 `v`, 또는 `routebox route default`). |
| 라우팅한 사이트가 `502` | 그 라우트의 업스트림이 죽어 있습니다. 배지가 `일부 장애`/`재연결 중`이고 하단에 어느 업스트림인지 보입니다. 의도적으로 다른 경로로 새지 않습니다. |
| 새 라우트를 무시하는 것 같음 | 브라우저가 기존 연결을 재사용하고 있습니다. 규칙은 **새 연결**부터 적용됩니다. 탭을 새로 고치거나 잠시 기다리세요. Firefox 설정 4번도 확인하세요. |
| 서비스 일부가 여전히 직접 연결됨 | 서비스가 라우트보다 많은 도메인을 씁니다. 실시간 연결의 `DIRECT` 항목을 보고 추가하세요. |

상태 배지:

| 배지 | 의미 |
|---|---|
| `연결됨` (CONNECTED) | 프록시가 떠 있고 모든 업스트림이 정상 |
| `일부 장애` (DEGRADED) | 프록시는 정상이지만 업스트림 하나 이상에 문제 (다른 라우트는 계속 동작) |
| `재연결 중` (RECONNECTING) | managed ssh가 연결 또는 재연결 중 |
| `연결 안 됨` (DISCONNECTED) | 업스트림이 없거나 프록시가 떠 있지 않음 |

## 15. 보안

- 기본 listen 주소는 `127.0.0.1`입니다. `0.0.0.0` 등 loopback이 아닌 주소를 쓰면 TUI와 CLI에 **"RouteBox is listening on a non-loopback address. This may expose an open proxy to your network."** 경고가 뜹니다. RouteBox에는 인증이 없으므로 외부에 열면 SSH 서버들이 **오픈 프록시**가 됩니다.
- TLS를 복호화하지 않고, 인증서를 만들거나 설치하지 않으며, SNI도 건드리지 않습니다.
- 로그·TUI·이벤트에는 **`host:port` 만** 남깁니다. URL 경로, query string, 헤더(Authorization, Proxy-Authorization, Cookie)는 기록하지 않습니다. 일반 HTTP를 전달할 때 `Proxy-Authorization` 같은 hop-by-hop 헤더는 넘기지 않습니다.
- 개인 키는 저장하지 않고 경로만 저장합니다. 비밀번호 인증 UI는 없습니다.
- 호스트 키는 절대 자동으로 수락하지 않습니다. 지문을 보여 주고 사용자가 확인해야만 저장하며, 달라진 키는 신뢰를 제안하지 않습니다.
- `-`로 시작하는 ssh 호스트·사용자는 거부하고, 호스트 앞에 `--`를 넣어 옵션 주입을 막습니다.
- 설정 파일(`0600`)과 제어 소켓(`0700` 디렉터리 안의 `0600`)은 본인 계정만 접근할 수 있습니다. 제어 소켓을 쓸 수 있으면 RouteBox를 조종할 수 있으므로 TCP로는 열지 않습니다.
- macOS 백그라운드 서비스는 모든 연결의 `host:port`를 `~/Library/Logs/RouteBox/routebox.log` (`0600`, 10 MB에서 교체, 백업 하나)에 기록합니다. `routebox service uninstall`은 로그를 지우지 않으므로, 서비스를 그만 쓰면 폴더를 직접 지우세요.
- 어디에 접속했는지 남기고 싶지 않다면 연결 기록을 끄세요(TUI의 `o` 또는 `routebox log off`). 그러면 RouteBox는 TUI, 제어 소켓, `--no-tui`·`--log-file`·서비스 로그 어디에도 연결을 기록하지 않고 개수만 셉니다. `c`나 `routebox log clear`는 메모리에 있는 기록을 지웁니다(로그 파일은 건드리지 않음).
- 알아둘 점: 프록시를 우회하는 브라우저 기능(DNS-over-HTTPS, 프리페치)과 프록시 설정을 무시하는 앱은 RouteBox를 거치지 않습니다. WebRTC 같은 UDP 트래픽은 HTTP 프록시로 전달되지 않습니다.

## 16. 아키텍처

```
cmd/routebox/          Cobra CLI, TUI / --no-tui / attach 모드 조립, 제어 소켓 클라이언트
internal/
  core/                App: config·router·proxy·업스트림별 ssh·stats·events를 묶는 단일 진입점
                       (TUI·CLI·제어 소켓이 모두 같은 메서드를 호출)
  config/              Config/Upstream 타입, 검증, 예전 형식 이전, atomic 저장, Store
  router/              입력 정규화, 레이블 워크 매처(가장 구체적인 규칙 우선), via, preset
  proxy/               HTTP CONNECT / 일반 HTTP 서버, 업스트림별 dial, 양방향 relay
  socks/               최소 SOCKS5 클라이언트 (hostname은 항상 ATYP 0x03)
    sockstest/         테스트용 인프로세스 SOCKS5 서버 (받은 주소를 그대로 기록)
  ssh/                 ssh 자식 하나 감독: 준비 감지, 재연결, 종료와 회수
  control/             유닉스 소켓 HTTP API + 단일 인스턴스 잠금, NDJSON 이벤트 스트림,
                       Remote (실행 중인 인스턴스에 붙은 TUI 용, 스냅샷을 폴링하는 Backend)
  service/             launchd LaunchAgent / systemd --user unit 생성과 제어
  logfile/             백그라운드 서비스용 크기 기준 교체 로그 파일
  events/              non-blocking 이벤트 버스 (느린 구독자는 이벤트를 잃고 프록시는 막히지 않음)
  stats/               atomic 카운터
  logbuf/              제네릭 링 버퍼 (최근 연결 500개)
  tui/                 Bubble Tea 모델·업데이트·뷰
    components/        props-in / string-out 표현 컴포넌트
    i18n/              영어 / 한국어 문구
```

```
          ┌──────────── TUI ────────────┐      ┌──── CLI ─────┐
          │ Bubble Tea (구독 + 폴링)     │      │ route/…/ssh  │
          └──────────────┬──────────────┘      └──────┬───────┘
                         │ 메서드 호출 또는 socket    │ unix socket (실행 중)
                         ▼                            ▼ 또는 직접 호출 (미실행)
 ┌──────────────────────────────── core.App ────────────────────────────────┐
 │ config.Store ─► router.Router (atomic 교체)                               │
 │ proxy.Server ─► proxy.Transport ─┬─ direct     : net.Dialer (OS resolver) │
 │                                  └─ via <이름> : socks.Dialer → 그 SOCKS  │
 │ ssh.Manager × N (managed 업스트림마다 하나)   업스트림별 상태 확인          │
 │ stats.Stats (atomic)   events.Bus ─► 구독자   logbuf.Ring                 │
 └───────────────────────────────────────────────────────────────────────────┘
```

설계상 지킨 것들:

- 라우팅 테이블은 **불변 스냅샷**을 `atomic.Pointer`로 교체합니다. 조회에 락이 없고, 변경은 다음 연결부터 보입니다.
- 클라이언트가 **CONNECT 헤더 바로 뒤에** 보낸 바이트(보통 TLS ClientHello)를 버리지 않고 넘깁니다.
- **half-close**: 한쪽이 EOF를 보내면 반대쪽의 쓰기 방향만 닫고 다른 방향은 계속 흐릅니다. 그래서 SOCKS 클라이언트는 `CloseWrite`가 되는 원본 TCP 연결을 돌려줍니다.
- **idle timeout**은 방향별 read deadline이 아니라 양방향이 함께 쓰는 "마지막 활동 시각"과 watchdog으로 잽니다. 다운로드만 있는 스트림이 업로드가 없다는 이유로 끊기지 않습니다.
- **graceful shutdown**: 새 연결을 받지 않고, 진행 중인 터널에 1초 유예를 준 뒤 나머지를 닫고, 모든 핸들러 goroutine이 끝날 때까지 기다립니다. 모든 ssh 자식이 회수된 뒤에 프로세스가 끝납니다.

테스트로 고정한 성질:

- 업스트림으로 가는 연결은 로컬 resolver를 부르지 않고 hostname을 그대로 SOCKS에 넘깁니다. 직접 연결 dialer에 넣은 카운팅 resolver가 **업스트림 라우트에서는 0회, 직접 라우트에서는 1회 이상**(양성 대조군)을 보고, 로컬에서 해석될 수 없는 `*.invalid` 이름도 SOCKS로는 연결됩니다.
- **각 라우트는 자기 업스트림에만 닿습니다**: SOCKS 서버 두 개를 두면 각자 자기 호스트만 받고, 없는 업스트림을 가리키는 라우트는 어느 쪽도 건드리지 않고 `502`를 받습니다.
- CONNECT 헤더 뒤 바이트 유실 없음, half-close, idle timeout, graceful shutdown, 클라이언트/업스트림 비정상 종료, IPv6 리터럴, 포트 없는 CONNECT, 잘못된 요청, 포트 충돌.
- ssh 감독은 테스트 바이너리 자신을 가짜 `ssh`로 실행해 연결, 인증 실패, 재연결, 재시작, SIGTERM을 무시할 때의 SIGKILL 승격, **좀비 없음**, 실행 중 업스트림을 추가·삭제하면 정확히 그 업스트림의 ssh만 시작·중지되는 것을 확인합니다.
- 모든 TUI 문구에 형식 동사가 같은 한국어 번역이 있고, 두 언어 모두 모든 레이아웃이 터미널 크기 안에 들어갑니다.

알려진 제한:

- macOS에는 Linux의 `Pdeathsig`가 없어서, RouteBox가 `kill -9`로 죽으면 ssh 자식이 남을 수 있습니다. 정상 종료, SIGTERM, SIGINT, SIGHUP에서는 항상 정리됩니다.
- 일반 HTTP의 `Upgrade`(암호화되지 않은 `ws://`)는 지원하지 않습니다. HTTPS와 `wss://`는 CONNECT 라 문제없습니다.
- SOCKS5 사용자/비밀번호 인증은 지원하지 않습니다(`ssh -D`가 쓰는 no-auth 전용).

## 라이선스

[MIT](LICENSE). 배포 바이너리에는 MIT, BSD-3-Clause, Apache-2.0 라이선스의 서드파티 코드(Charmbracelet 라이브러리, Cobra, golang.org/x)가 포함됩니다. 보안 문제 제보는 [SECURITY.md](SECURITY.md)를 참고하세요.
