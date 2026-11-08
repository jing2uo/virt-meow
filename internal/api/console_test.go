package api

import "testing"

func TestOpenConsoleWindow(t *testing.T) {
	var gotURL, gotTitle string
	s := &Service{c: &Core{opts: Options{OpenWindow: func(u, title string) error { gotURL, gotTitle = u, title; return nil }}}}
	if err := s.OpenConsoleWindow("lab one", "web/01 é", "vnc"); err != nil {
		t.Fatal(err)
	}
	if gotURL != "/?solo#/lab%20one/console/web%2F01%20%C3%A9/vnc" || gotTitle != "web/01 é" {
		t.Errorf("window %q titled %q", gotURL, gotTitle)
	}
	if err := s.OpenConsoleWindow("lab", "web01", "rdp"); err == nil {
		t.Error("a console kind virt-meow doesn't have opened a window")
	}
	if u, _ := SoloConsoleURL("lab", "web01", ""); u != "/?solo#/lab/console/web01" {
		t.Errorf("the first console: %q", u)
	}
	s.c.opts.OpenWindow = nil
	if err := s.OpenConsoleWindow("lab", "web01", "vnc"); err == nil {
		t.Error("a browser's page opened a window")
	}
}
