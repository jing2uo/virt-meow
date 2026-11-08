package api

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/jing2uo/virt-meow/internal/conn"
)

// Prompt asks the page for a secret; the page replies with Service.Answer.
// Under serve every open page is asked, and the first answer counts.
type Prompt struct {
	ID   string `json:"id"`
	Kind string `json:"kind"` // password, passphrase
	User string `json:"user,omitempty"`
	Addr string `json:"addr,omitempty"`
	Key  string `json:"key,omitempty"` // the key file, for a passphrase
	// CanRemember offers to keep the answer in the OS keychain (of the
	// computer virt-meow runs on).
	CanRemember bool `json:"can_remember,omitempty"`
	// Refused says the remembered answer didn't work and is forgotten.
	Refused bool `json:"refused,omitempty"`
}

// NotRemembered is an answer the user asked to remember that the keychain
// didn't take.
type NotRemembered struct {
	Kind  string `json:"kind"`
	User  string `json:"user,omitempty"`
	Addr  string `json:"addr,omitempty"`
	Key   string `json:"key,omitempty"`
	Error string `json:"error"`
}

type answer struct {
	value    string
	ok       bool
	remember bool
}

// prompter implements conn.Prompter by asking the page and waiting for its
// answer. An SSH handshake waits on it, bounded by the connection's own
// timeout and by promptTimeout here.
type prompter struct {
	emit    func(string, any)
	mu      sync.Mutex
	waiting map[string]chan answer
}

const promptTimeout = 2 * time.Minute

var errCancelled = errors.New("cancelled")

func (p *prompter) Ask(q conn.Question) (conn.Answer, error) {
	return p.ask(Prompt{Kind: q.Kind, User: q.User, Addr: q.Addr, Key: q.Key, CanRemember: q.CanRemember, Refused: q.Refused})
}

func (p *prompter) NotRemembered(s conn.Secret, err error) {
	if p.emit != nil {
		p.emit(EvNotRemembered, NotRemembered{Kind: s.Kind, User: s.User, Addr: s.Addr, Key: s.Key, Error: err.Error()})
	}
}

func (p *prompter) ask(q Prompt) (conn.Answer, error) {
	if p.emit == nil {
		return conn.Answer{}, conn.ErrNoPrompt
	}
	b := make([]byte, 8)
	rand.Read(b)
	q.ID = hex.EncodeToString(b)
	ch := make(chan answer, 1)
	p.mu.Lock()
	p.waiting[q.ID] = ch
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.waiting, q.ID)
		p.mu.Unlock()
	}()
	p.emit(EvPrompt, q)
	select {
	case a := <-ch:
		if !a.ok {
			return conn.Answer{}, errCancelled
		}
		return conn.Answer{Value: a.value, Remember: a.remember && q.CanRemember}, nil
	case <-time.After(promptTimeout):
		return conn.Answer{}, errCancelled
	}
}

// answer delivers the first answer to a prompt; later ones (another page's,
// under serve) find nothing waiting.
func (p *prompter) answer(id, value string, ok, remember bool) {
	p.mu.Lock()
	ch := p.waiting[id]
	delete(p.waiting, id)
	p.mu.Unlock()
	if ch != nil {
		ch <- answer{value: value, ok: ok, remember: remember}
	}
}
