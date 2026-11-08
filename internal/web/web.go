// Package web is virt-mm serve: the page (internal/ui) in a browser, with
// api.Service over HTTP and its events over a WebSocket (docs/adr/0003).
//
//	POST /api/<Method>   the JSON array of a Service method's arguments;
//	                     its result as JSON, or {"error": "…"} with 500
//	GET  /api/events     WebSocket: {"name": "…", "data": …} per event
//	POST /api/upload     a file's bytes into a pool (?connection&pool&name)
//	GET  /api/download   a volume's bytes, as a file to save (?connection&pool&name)
//	/console/<token>     a console's WebSocket (internal/console)
//	/                    the page
//
// With a login (UseLogin), all of it needs a session but these:
//
//	/login               the login page (and the files it loads)
//	POST /api/login      {"password": "…"}: a session cookie
//	POST /api/logout     ends the session
//	GET  /api/session    {"login": true|false}: whether the server has one
//
// Either way only its own pages can use it: a request must name the server
// itself as its Host (no DNS rebinding) and, when it comes from a page,
// have that same origin; calls need a JSON content type, which no other
// site's form can send without the browser asking first (and the server
// never says yes).
package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/jing2uo/virt-meow/internal/api"
)

// Server serves one api.Core.
type Server struct {
	core    *api.Core
	page    fs.FS
	methods map[string]reflect.Value
	hosts   map[string]bool // Host values accepted; nil for any
	login   *Login          // nil: no login

	mu   sync.Mutex
	subs map[chan []byte]bool
}

// New makes a server for core, its page from page. hosts are the Host
// header values it answers to ("127.0.0.1:8080", "localhost:8080"); nil
// means any, for a server reached under names it can't know (behind a
// proxy, on a LAN).
func New(core *api.Core, page fs.FS, hosts []string) *Server {
	s := &Server{core: core, page: page, methods: map[string]reflect.Value{}, subs: map[chan []byte]bool{}}
	v := reflect.ValueOf(core.Service())
	for i := 0; i < v.NumMethod(); i++ {
		s.methods[v.Type().Method(i).Name] = v.Method(i)
	}
	if hosts != nil {
		s.hosts = map[string]bool{}
		for _, h := range hosts {
			s.hosts[strings.ToLower(h)] = true
		}
	}
	return s
}

// UseLogin makes every request but the login page's need a session.
func (s *Server) UseLogin(l *Login) { s.login = l }

// loginFiles are what the login page loads before there is a session.
var loginFiles = map[string]bool{"/login.html": true, "/login.js": true, "/i18n.js": true, "/style.css": true, "/favicon.png": true}

// LoopbackHosts are the names a server listening on addr (a loopback
// address and port) is reached by from the same computer.
func LoopbackHosts(addr string) []string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil
	}
	return []string{"127.0.0.1:" + port, "localhost:" + port, "[::1]:" + port}
}

// Emit sends an event to every open page; give it to api.Options.
func (s *Server) Emit(name string, data any) {
	b, err := json.Marshal(struct {
		Name string `json:"name"`
		Data any    `json:"data"`
	}{name, data})
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.subs {
		select {
		case ch <- b:
		default: // a page that doesn't keep up misses events; it reloads its lists on reconnect
		}
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.allowed(w, r) {
		return
	}
	if !s.authorized(w, r) {
		return
	}
	switch p := r.URL.Path; {
	case p == "/api/events":
		s.events(w, r)
	case p == "/api/upload":
		s.upload(w, r)
	case p == "/api/download":
		s.download(w, r)
	case strings.HasPrefix(p, "/api/"):
		s.call(w, r, strings.TrimPrefix(p, "/api/"))
	case strings.HasPrefix(p, "/console/"):
		s.core.Consoles().ServeHTTP(w, r)
	default:
		w.Header().Set("Cache-Control", "no-cache")
		http.FileServerFS(s.page).ServeHTTP(w, r)
	}
}

// allowed is the guard described in the package comment.
func (s *Server) allowed(w http.ResponseWriter, r *http.Request) bool {
	if s.hosts != nil && !s.hosts[strings.ToLower(r.Host)] {
		http.Error(w, "unknown host", http.StatusMisdirectedRequest)
		return false
	}
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		if err != nil || !strings.EqualFold(u.Host, r.Host) {
			http.Error(w, "cross-origin request refused", http.StatusForbidden)
			return false
		}
	}
	return true
}

// authorized answers what needs no session and refuses what needs one it
// lacks; true means r goes on.
func (s *Server) authorized(w http.ResponseWriter, r *http.Request) bool {
	p := r.URL.Path
	switch {
	case p == "/api/session":
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"login": s.login != nil})
		return false
	case s.login == nil:
		if p == "/login" {
			http.Redirect(w, r, "/", http.StatusFound)
			return false
		}
		return true
	case p == "/api/login":
		s.login.serveLogin(w, r)
		return false
	case p == "/api/logout":
		s.login.serveLogout(w, r)
		return false
	case p == "/login":
		r.URL.Path = "/login.html"
		fallthrough
	case loginFiles[p]:
		w.Header().Set("Cache-Control", "no-cache")
		http.FileServerFS(s.page).ServeHTTP(w, r)
		return false
	case s.login.valid(r):
		return true
	case p == "/" || p == "/index.html":
		http.Redirect(w, r, "/login", http.StatusFound)
		return false
	}
	writeError(w, http.StatusUnauthorized, errors.New("login required"))
	return false
}

// jsonBody reports whether r says its body is JSON.
func jsonBody(r *http.Request) bool {
	ct, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";")
	return strings.TrimSpace(ct) == "application/json"
}

func writeError(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

var (
	errType = reflect.TypeFor[error]()
	ctxType = reflect.TypeFor[context.Context]()
)

// call runs a Service method with the request's JSON array as its
// arguments.
func (s *Server) call(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, errors.New("POST only"))
		return
	}
	if !jsonBody(r) {
		writeError(w, http.StatusUnsupportedMediaType, errors.New("application/json only"))
		return
	}
	m, ok := s.methods[name]
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("no method %s", name))
		return
	}
	var raw []json.RawMessage
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&raw); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("arguments: %w", err))
		return
	}
	// A method that takes a context first (as Wails passes the calling
	// window in one) gets the request's; the page's arguments are the rest.
	t, args := m.Type(), []reflect.Value{}
	if t.NumIn() > 0 && t.In(0) == ctxType {
		args = append(args, reflect.ValueOf(r.Context()))
	}
	if len(args)+len(raw) != t.NumIn() {
		writeError(w, http.StatusBadRequest, fmt.Errorf("%s takes %d arguments, not %d", name, t.NumIn()-len(args), len(raw)))
		return
	}
	for i, a := range raw {
		p := reflect.New(t.In(len(args)))
		if err := json.Unmarshal(a, p.Interface()); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("argument %d: %w", i+1, err))
			return
		}
		args = append(args, p.Elem())
	}
	out := m.Call(args)
	var result any
	for _, o := range out {
		if o.Type() == errType {
			if !o.IsNil() {
				writeError(w, http.StatusInternalServerError, o.Interface().(error))
				return
			}
			continue
		}
		result = o.Interface()
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// events streams every event to the page until it goes.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, nil) // checks the origin itself, too
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ch := make(chan []byte, 256)
	s.mu.Lock()
	s.subs[ch] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}()
	ctx := ws.CloseRead(r.Context()) // the page sends nothing; this notices it going
	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()
	for {
		select {
		case b := <-ch:
			wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := ws.Write(wctx, websocket.MessageText, b)
			cancel()
			if err != nil {
				return
			}
		case <-ping.C:
			pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := ws.Ping(pctx)
			cancel()
			if err != nil {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// upload copies the request's body into a new volume; its progress comes
// as "upload" events, as for the window's picked files. The answer comes
// when the copy ends.
func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, errors.New("POST only"))
		return
	}
	// Not a type a form can send, so another site's page can't post here
	// without the browser asking first.
	if ct := r.Header.Get("Content-Type"); ct != "application/octet-stream" {
		writeError(w, http.StatusUnsupportedMediaType, errors.New("application/octet-stream only"))
		return
	}
	q := r.URL.Query()
	id, pool, name := q.Get("connection"), q.Get("pool"), q.Get("name")
	if id == "" || pool == "" || name == "" || strings.ContainsAny(name, "/\\") {
		writeError(w, http.StatusBadRequest, errors.New("connection, pool and name (a file name) are needed"))
		return
	}
	size := r.ContentLength
	if v := q.Get("size"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			size = n
		}
	}
	if size <= 0 {
		writeError(w, http.StatusLengthRequired, errors.New("the file's size is needed"))
		return
	}
	if err := s.core.UploadFrom(r.Context(), id, pool, name, r.Body, size); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"pool": pool, "name": name})
}

// download sends a volume as a file for the browser to save. It changes
// nothing, so a GET (a link the page follows) is enough, under the same
// guard as the rest; the response is no other site's to read.
func (s *Server) download(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, errors.New("GET only"))
		return
	}
	q := r.URL.Query()
	id, pool, name := q.Get("connection"), q.Get("pool"), q.Get("name")
	if id == "" || pool == "" || name == "" {
		writeError(w, http.StatusBadRequest, errors.New("connection, pool and name are needed"))
		return
	}
	lw := &lazyWriter{w: w, name: name}
	if _, err := s.core.DownloadTo(r.Context(), id, pool, name, lw); err != nil {
		if !lw.started {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		// Part of it went: break the connection, so the browser says the
		// download failed rather than keeping a short file.
		panic(http.ErrAbortHandler)
	}
	if !lw.started { // an empty volume
		lw.start()
	}
}

// lazyWriter answers 200 with the file's headers at the first byte, so an
// error before it can still be an error.
type lazyWriter struct {
	w       http.ResponseWriter
	name    string
	started bool
}

func (l *lazyWriter) start() {
	l.started = true
	h := l.w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(l.name))
	h.Set("X-Content-Type-Options", "nosniff")
	l.w.WriteHeader(http.StatusOK)
}

func (l *lazyWriter) Write(p []byte) (int, error) {
	if !l.started {
		l.start()
	}
	return l.w.Write(p)
}
