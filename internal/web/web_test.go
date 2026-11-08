package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coder/websocket"

	"github.com/jing2uo/virt-meow/internal/api"
)

// server is a Server with a scratch config, served on a loopback port and
// answering only to that port's names.
func server(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	t.Setenv("VIRT_MM_CONFIG", t.TempDir())
	var s *Server
	core, err := api.New(api.Options{Version: "test", Mode: api.Web, Emit: func(n string, d any) { s.Emit(n, d) }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(core.Close)
	page := fstest.MapFS{"index.html": {Data: []byte("<title>virt-meow</title>")}}
	ts := httptest.NewUnstartedServer(nil)
	s = New(core, page, LoopbackHosts(ts.Listener.Addr().String()))
	ts.Config.Handler = s
	ts.Start()
	t.Cleanup(ts.Close)
	return s, ts
}

func post(t *testing.T, ts *httptest.Server, path, body string, header map[string]string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	if h := header["Host"]; h != "" {
		req.Host = h
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r.StatusCode, strings.TrimSpace(string(b))
}

func TestCalls(t *testing.T) {
	_, ts := server(t)
	if code, body := post(t, ts, "/api/Connections", "[]", nil); code != 200 || body != "[]" {
		t.Fatalf("Connections: %d %s", code, body)
	}
	code, body := post(t, ts, "/api/AddConnection", `["lab","qemu:///session",false]`, nil)
	var c struct{ ID, Name, URI string }
	if code != 200 || json.Unmarshal([]byte(body), &c) != nil || c.Name != "lab" || c.ID == "" {
		t.Fatalf("AddConnection: %d %s", code, body)
	}
	if code, body := post(t, ts, "/api/Env", "[]", nil); code != 200 || !strings.Contains(body, `"mode":"web"`) || !strings.Contains(body, `"lang":""`) {
		t.Fatalf("Env: %d %s", code, body)
	}
	for what, c := range map[string]struct {
		path, body string
		want       int
	}{
		"no such method":     {"/api/Nope", "[]", 404},
		"too few arguments":  {"/api/AddConnection", `["lab"]`, 400},
		"a wrong type":       {"/api/AddConnection", `[1,2,3]`, 400},
		"not an array":       {"/api/Connections", `{}`, 400},
		"a Go error":         {"/api/AddConnection", `["x","nonsense",false]`, 500},
		"not exported to it": {"/api/Close", "[]", 404},
	} {
		if code, body := post(t, ts, c.path, c.body, nil); code != c.want || !strings.Contains(body, `"error"`) {
			t.Errorf("%s: %d %s, want %d and an error", what, code, body, c.want)
		}
	}
}

// What another site, or a name rebound to 127.0.0.1, can't do.
func TestGuard(t *testing.T) {
	_, ts := server(t)
	for what, c := range map[string]struct {
		header map[string]string
		want   int
	}{
		"another site's page":    {map[string]string{"Origin": "https://evil.example"}, 403},
		"a form's content type":  {map[string]string{"Content-Type": "text/plain"}, 415},
		"a rebound name":         {map[string]string{"Host": "evil.example:80"}, 421},
		"its own page":           {map[string]string{"Origin": ts.URL}, 200},
		"a tool, without origin": {map[string]string{"Origin": ""}, 200},
	} {
		if code, body := post(t, ts, "/api/Connections", "[]", c.header); code != c.want {
			t.Errorf("%s: %d %s, want %d", what, code, body, c.want)
		}
	}
	r, err := http.Get(ts.URL + "/api/Connections")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 405 {
		t.Errorf("GET a method: %d", r.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/upload?connection=x&pool=p&name=a.iso", strings.NewReader("x"))
	req.Header.Set("Content-Type", "multipart/form-data")
	if r, err := http.DefaultClient.Do(req); err != nil || r.StatusCode != 415 {
		t.Errorf("an upload from a form: %v %v", r.StatusCode, err)
	}
}

func TestPageAndEvents(t *testing.T) {
	s, ts := server(t)
	r, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if !strings.Contains(string(b), "virt-meow") {
		t.Fatalf("page: %s", b)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/events"
	if _, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {"https://evil.example"}}}); err == nil {
		t.Fatal("another site's page opened the event stream")
	}
	ws, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	// The subscription is registered by the handler; wait for it.
	for i := 0; ; i++ {
		s.mu.Lock()
		n := len(s.subs)
		s.mu.Unlock()
		if n == 1 {
			break
		}
		if i > 100 {
			t.Fatal("not subscribed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.Emit("connection", map[string]string{"id": "x"})
	_, msg, err := ws.Read(ctx)
	if err != nil || string(msg) != `{"name":"connection","data":{"id":"x"}}` {
		t.Fatalf("event: %s %v", msg, err)
	}
}

// GET /api/download: the guard, and errors before any byte as JSON.
func TestDownloadGuard(t *testing.T) {
	_, ts := server(t)
	get := func(path string, header map[string]string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		for k, v := range header {
			req.Header.Set(k, v)
		}
		if h := header["Host"]; h != "" {
			req.Host = h
		}
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		return r.StatusCode, strings.TrimSpace(string(b))
	}
	const q = "/api/download?connection=nope&pool=default&name=a.qcow2"
	for what, c := range map[string]struct {
		path   string
		header map[string]string
		code   int
	}{
		"another Host":       {q, map[string]string{"Host": "evil.example:80"}, http.StatusMisdirectedRequest},
		"another origin":     {q, map[string]string{"Origin": "http://evil.example"}, http.StatusForbidden},
		"no name":            {"/api/download?connection=nope&pool=default", nil, http.StatusBadRequest},
		"no such connection": {q, nil, http.StatusInternalServerError},
	} {
		if code, body := get(c.path, c.header); code != c.code {
			t.Errorf("%s: %d %s, want %d", what, code, body, c.code)
		}
	}
	if code, body := get(q, nil); !strings.Contains(body, `"error"`) {
		t.Errorf("an error is JSON: %d %s", code, body)
	}
	if code, _ := post(t, ts, q, "", nil); code != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", code)
	}
}

// The login: what needs a session and what doesn't, a session from the
// right password, the cookie's flags, logout, and the rate limit.
func TestLogin(t *testing.T) {
	hash, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	l, err := NewLogin(hash, true)
	if err != nil {
		t.Fatal(err)
	}
	page := fstest.MapFS{"index.html": {Data: []byte("page")}, "login.html": {Data: []byte("login")}, "app.js": {Data: []byte("js")}}
	core, err := api.New(api.Options{Mode: api.Web})
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	s := New(core, page, nil)
	s.UseLogin(l)
	do := func(method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	if w := do("GET", "/", "", nil); w.Code != http.StatusFound || w.Header().Get("Location") != "/login" {
		t.Errorf("/ without a session: %d %s", w.Code, w.Header().Get("Location"))
	}
	if w := do("GET", "/login", "", nil); w.Code != 200 || w.Body.String() != "login" {
		t.Errorf("/login: %d %q", w.Code, w.Body.String())
	}
	for _, p := range []string{"/app.js", "/api/Connections", "/api/events", "/console/x", "/api/upload"} {
		if w := do("POST", p, "[]", nil); w.Code != http.StatusUnauthorized {
			t.Errorf("%s without a session: %d", p, w.Code)
		}
	}
	if w := do("GET", "/api/session", "", nil); w.Body.String() != "{\"login\":true}\n" {
		t.Errorf("/api/session: %q", w.Body.String())
	}
	if w := do("POST", "/api/login", `{"password":"wrong"}`, nil); w.Code != http.StatusUnauthorized {
		t.Errorf("a wrong password: %d", w.Code)
	}
	w := do("POST", "/api/login", `{"password":"correct horse"}`, nil)
	cs := w.Result().Cookies()
	if w.Code != 200 || len(cs) != 1 || !cs[0].HttpOnly || !cs[0].Secure || cs[0].SameSite != http.SameSiteStrictMode || len(cs[0].Value) != 64 {
		t.Fatalf("login: %d %+v", w.Code, cs)
	}
	session := cs[0]
	if w := do("POST", "/api/Connections", "[]", session); w.Code != 200 {
		t.Errorf("a call with the session: %d %s", w.Code, w.Body.String())
	}
	if w := do("GET", "/", "", session); w.Code != 200 || w.Body.String() != "page" {
		t.Errorf("the page with the session: %d", w.Code)
	}
	do("POST", "/api/logout", "{}", session)
	if w := do("POST", "/api/Connections", "[]", session); w.Code != http.StatusUnauthorized {
		t.Errorf("a call after logout: %d", w.Code)
	}
	// Five failures: the sixth try waits, even with the right password.
	for range 5 {
		do("POST", "/api/login", `{"password":"nope"}`, nil)
	}
	if w := do("POST", "/api/login", `{"password":"correct horse"}`, nil); w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Errorf("after five failures: %d %q", w.Code, w.Header().Get("Retry-After"))
	}
	l.now = func() time.Time { return time.Now().Add(failWindow) }
	if w := do("POST", "/api/login", `{"password":"correct horse"}`, nil); w.Code != 200 {
		t.Errorf("a minute later: %d", w.Code)
	}
	if _, err := HashPassword("short"); err == nil {
		t.Error("a short password")
	}
	if _, err := NewLogin([]byte("plain"), false); err == nil {
		t.Error("a login from what isn't a hash")
	}
}
