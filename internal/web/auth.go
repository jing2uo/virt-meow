package web

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Login is virt-mm serve's one user: a password whose bcrypt hash it holds,
// and the sessions logged in with it, each a random token in a cookie
// (HttpOnly, SameSite=Strict, Secure under TLS). Sessions live in memory:
// a restart logs everyone out.
type Login struct {
	hash   []byte
	secure bool
	now    func() time.Time

	mu       sync.Mutex
	sessions map[string]time.Time // token → when it expires
	fails    map[string][]time.Time
}

const (
	sessionCookie = "virtmm_session"
	// sessionIdle is how long a session lives unused; each request renews it.
	sessionIdle = 7 * 24 * time.Hour
	// maxFails failed logins from one address within failWindow make it
	// wait until the oldest is that old.
	maxFails   = 5
	failWindow = time.Minute
	// MinPassword is the shortest password virt-mm serve takes.
	MinPassword = 8
)

// HashPassword is the bcrypt hash of a new password for the login.
func HashPassword(pw string) ([]byte, error) {
	if len(pw) < MinPassword {
		return nil, errors.New("the password: at least 8 characters")
	}
	return bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
}

// NewLogin is the login for the password whose bcrypt hash is hash; secure
// when the server is TLS, so the cookie never goes out over plain HTTP.
func NewLogin(hash []byte, secure bool) (*Login, error) {
	if _, err := bcrypt.Cost(hash); err != nil {
		return nil, errors.New("the stored password isn't a bcrypt hash: set it again with virt-mm serve --set-password")
	}
	return &Login{hash: hash, secure: secure, now: time.Now, sessions: map[string]time.Time{}, fails: map[string][]time.Time{}}, nil
}

// valid reports whether r carries a live session, and renews it.
func (l *Login) valid(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	exp, ok := l.sessions[c.Value]
	now := l.now()
	if !ok || now.After(exp) {
		delete(l.sessions, c.Value)
		return false
	}
	l.sessions[c.Value] = now.Add(sessionIdle)
	return true
}

func (l *Login) cookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: sessionCookie, Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: l.secure}
}

// client is who a login attempt comes from, for the rate limit: the peer's
// address (behind a proxy, the proxy's).
func client(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// wait is how long who must wait before trying again; 0 when it may.
func (l *Login) wait(who string) time.Duration {
	now := l.now()
	recent := l.fails[who][:0]
	for _, t := range l.fails[who] {
		if now.Sub(t) < failWindow {
			recent = append(recent, t)
		}
	}
	l.fails[who] = recent
	if len(recent) < maxFails {
		return 0
	}
	return failWindow - now.Sub(recent[0])
}

// serveLogin is POST /api/login, {"password": "…"}: a new session's cookie,
// or 401; 429 with Retry-After while the address has failed too often.
func (l *Login) serveLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, errors.New("POST only"))
		return
	}
	if !jsonBody(r) {
		writeError(w, http.StatusUnsupportedMediaType, errors.New("a JSON body"))
		return
	}
	var in struct{ Password string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	who := client(r)
	l.mu.Lock()
	wait := l.wait(who)
	l.mu.Unlock()
	if wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, errors.New("too many failed logins: wait a minute"))
		return
	}
	if bcrypt.CompareHashAndPassword(l.hash, []byte(in.Password)) != nil {
		l.mu.Lock()
		l.fails[who] = append(l.fails[who], l.now())
		l.mu.Unlock()
		writeError(w, http.StatusUnauthorized, errors.New("wrong password"))
		return
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	token := hex.EncodeToString(b)
	l.mu.Lock()
	delete(l.fails, who)
	l.sessions[token] = l.now().Add(sessionIdle)
	l.mu.Unlock()
	http.SetCookie(w, l.cookie(token, int(sessionIdle.Seconds())))
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}

// serveLogout is POST /api/logout: the session ends here and in the browser.
func (l *Login) serveLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, errors.New("POST only"))
		return
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		l.mu.Lock()
		delete(l.sessions, c.Value)
		l.mu.Unlock()
	}
	http.SetCookie(w, l.cookie("", -1))
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}
