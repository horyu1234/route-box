package ssh

import "time"

type State int

const (
	// StateDisabled 는 RouteBox 가 ssh 를 관리하지 않는다는 뜻이다 (external SOCKS 모드거나 host 없음).
	StateDisabled State = iota
	StateStopped
	StateStarting
	StateConnected
	StateReconnecting
	StateFailed
)

func (s State) String() string {
	switch s {
	case StateStopped:
		return "stopped"
	case StateStarting:
		return "starting"
	case StateConnected:
		return "connected"
	case StateReconnecting:
		return "reconnecting"
	case StateFailed:
		return "failed"
	default:
		return "disabled"
	}
}

func (s State) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

func (s *State) UnmarshalText(b []byte) error {
	for st := StateDisabled; st <= StateFailed; st++ {
		if st.String() == string(b) {
			*s = st
			return nil
		}
	}
	*s = StateDisabled
	return nil
}

// Status 는 managed ssh 프로세스의 특정 시점 스냅샷이다.
type Status struct {
	State     State     `json:"state"`
	Host      string    `json:"host,omitempty"`
	PID       int       `json:"pid,omitempty"`
	Since     time.Time `json:"since"`
	Attempt   int       `json:"attempt,omitempty"`
	NextRetry time.Time `json:"next_retry,omitzero"`
	Err       string    `json:"error,omitempty"`
}
