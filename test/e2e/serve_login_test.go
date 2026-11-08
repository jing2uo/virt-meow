//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// selfSigned writes a certificate and key for 127.0.0.1 into dir.
func selfSigned(t *testing.T, dir string) (cert, key string, pool *x509.CertPool) {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "virt-mm-e2e"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalECPrivateKey(k)
	cert, key = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	os.WriteFile(key, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600)
	c, _ := x509.ParseCertificate(der)
	pool = x509.NewCertPool()
	pool.AddCert(c)
	return cert, key, pool
}

// virt-mm serve with a login, as a browser has it: the page and every call,
// the event and console WebSockets refused without a session; a wrong
// password, the right one, the session's cookie; logout; and over HTTPS,
// with the password set beforehand and kept hashed in the config.
func TestServeLogin(t *testing.T) {
	dir := t.TempDir()
	pw := filepath.Join(dir, "pw")
	os.WriteFile(pw, []byte("e2e-secret-1\n"), 0o600)
	where := startServe(t, virtmm(t, "serve", "--listen", "127.0.0.1:0", "--password-file", pw))
	base := "http://" + where.Listen

	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	post := func(path, body string) *http.Response {
		t.Helper()
		r, err := c.Post(base+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		return r
	}
	if r, _ := c.Get(base + "/"); r.StatusCode != http.StatusFound || r.Header.Get("Location") != "/login" {
		t.Errorf("the page without a session: %v", r.Status)
	}
	if r, _ := c.Get(base + "/login"); r.StatusCode != 200 {
		t.Errorf("the login page: %v", r.Status)
	}
	if r := post("/api/Connections", "[]"); r.StatusCode != http.StatusUnauthorized {
		t.Errorf("a call without a session: %v", r.Status)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ws := func() error {
		hdr := http.Header{}
		for _, ck := range jar.Cookies(mustURL(t, base)) {
			hdr.Add("Cookie", ck.String())
		}
		conn, _, err := websocket.Dial(ctx, "ws://"+where.Listen+"/api/events", &websocket.DialOptions{HTTPHeader: hdr})
		if err == nil {
			conn.CloseNow()
		}
		return err
	}
	if ws() == nil {
		t.Error("the event stream without a session")
	}
	if r, _ := c.Get(base + "/console/sometoken"); r.StatusCode != http.StatusUnauthorized {
		t.Errorf("a console without a session: %v", r.Status)
	}
	if r := post("/api/login", `{"password":"wrong"}`); r.StatusCode != http.StatusUnauthorized {
		t.Errorf("a wrong password: %v", r.Status)
	}
	r := post("/api/login", `{"password":"e2e-secret-1"}`)
	if r.StatusCode != 200 {
		t.Fatalf("login: %v", r.Status)
	}
	if sc := r.Header.Get("Set-Cookie"); !strings.Contains(sc, "HttpOnly") || !strings.Contains(sc, "SameSite=Strict") || strings.Contains(sc, "Secure") {
		t.Errorf("the cookie over HTTP: %s", sc)
	}
	if r := post("/api/Connections", "[]"); r.StatusCode != 200 {
		t.Errorf("a call with the session: %v", r.Status)
	}
	if err := ws(); err != nil {
		t.Errorf("the event stream with the session: %v", err)
	}
	if r, _ := c.Get(base + "/console/sometoken"); r.StatusCode == http.StatusUnauthorized {
		t.Error("a console refused with the session (an unknown token is the bridge's to refuse)")
	}
	post("/api/logout", "{}")
	if r := post("/api/Connections", "[]"); r.StatusCode != http.StatusUnauthorized {
		t.Errorf("a call after logout: %v", r.Status)
	}

	// The password set once and kept in the config; HTTPS, with a Secure
	// cookie.
	cfg := t.TempDir()
	set := virtmm(t, "serve", "--set-password", "--password-stdin")
	set.Env = append(set.Env, "VIRT_MM_CONFIG="+cfg)
	set.Stdin = strings.NewReader("kept-secret-2\n")
	if out, err := set.CombinedOutput(); err != nil {
		t.Fatalf("--set-password: %v: %s", err, out)
	}
	if b, err := os.ReadFile(filepath.Join(cfg, "serve-password")); err != nil || !bytes.HasPrefix(b, []byte("$2")) || bytes.Contains(b, []byte("kept-secret")) {
		t.Fatalf("the kept password: %q %v", b, err)
	}
	cert, key, pool := selfSigned(t, dir)
	cmd := virtmm(t, "serve", "--listen", "127.0.0.1:0", "--tls-cert", cert, "--tls-key", key)
	cmd.Env = append(cmd.Env, "VIRT_MM_CONFIG="+cfg)
	tw := startServe(t, cmd)
	if !strings.HasPrefix(tw.URL, "https://") {
		t.Errorf("the URL under TLS: %s", tw.URL)
	}
	hc := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	res, err := hc.Post("https://"+tw.Listen+"/api/login", "application/json", strings.NewReader(`{"password":"kept-secret-2"}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if sc := res.Header.Get("Set-Cookie"); res.StatusCode != 200 || !strings.Contains(sc, "Secure") {
		t.Errorf("login over HTTPS: %v %s", res.Status, sc)
	}

	// Beyond loopback with no password and nobody to ask: refused.
	cmd = virtmm(t, "serve", "--listen", "0.0.0.0:0")
	cmd.Stdin = strings.NewReader("")
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "--set-password") {
		t.Errorf("0.0.0.0 without a password: %v: %s", err, out)
	}
}

func mustURL(t *testing.T, s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
