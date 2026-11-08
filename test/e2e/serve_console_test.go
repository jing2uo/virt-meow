//go:build e2e

package e2e

import (
	"net/http"
	"strings"
	"testing"
)

// A console in a window of its own: under serve the page opens a browser
// tab on the solo page itself, so OpenConsoleWindow says so and opens
// nothing; that page and the console's modules are served.
func TestServeConsoleWindow(t *testing.T) {
	sv := serve(t)
	err := sv.try(nil, "OpenConsoleWindow", sv.conn, "web01", "vnc")
	if err == nil || !strings.Contains(err.Error(), "a browser opens a tab") {
		t.Errorf("OpenConsoleWindow under serve: %v", err)
	}
	for _, f := range []string{"/?solo", "/console.js", "/console-keys.js"} {
		r, err := http.Get(sv.base + f)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != 200 {
			t.Errorf("%s: %s", f, r.Status)
		}
	}
}
