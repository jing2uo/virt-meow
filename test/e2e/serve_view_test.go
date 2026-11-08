//go:build e2e

package e2e

import (
	"net/http"
	"strings"
	"testing"
)

// Where the page is: under serve its URL says it, so RememberView, the
// window's, refuses rather than change where the window opens next; the
// page's router and shortcuts are served.
func TestServeRememberView(t *testing.T) {
	sv := serve(t)
	err := sv.try(nil, "RememberView", "/"+sv.conn+"/machines")
	if err == nil || !strings.Contains(err.Error(), "only the desktop window remembers its view") {
		t.Errorf("RememberView under serve: %v", err)
	}
	for _, f := range []string{"/route.js", "/keys.js"} {
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
