package conn

import "testing"

func TestParse(t *testing.T) {
	cases := []struct {
		uri    string
		want   Target
		driver string
	}{
		{uri: "qemu:///system", want: Target{Driver: "qemu", Path: "/system", Transport: Local}, driver: "qemu:///system"},
		{uri: "qemu:///session", want: Target{Driver: "qemu", Path: "/session", Transport: Local}, driver: "qemu:///session"},
		{uri: "qemu+unix:///system?socket=/tmp/s", want: Target{Driver: "qemu", Path: "/system", Transport: Local, Socket: "/tmp/s"}, driver: "qemu:///system"},
		{uri: "qemu+ssh://root@kvm1:2200/system", want: Target{Driver: "qemu", Path: "/system", Transport: SSH, User: "root", Host: "kvm1", Port: "2200"}, driver: "qemu:///system"},
		{uri: "qemu+libssh2://kvm1/system?proxy=helper", want: Target{Driver: "qemu", Path: "/system", Transport: SSH, Host: "kvm1", Proxy: ProxyHelper}, driver: "qemu:///system"},
		{uri: "test+ssh://[::1]/default", want: Target{Driver: "test", Path: "/default", Transport: SSH, Host: "::1"}, driver: "test:///default"},
	}
	for _, c := range cases {
		got, err := Parse(c.uri)
		if err != nil {
			t.Errorf("Parse(%q): %v", c.uri, err)
			continue
		}
		c.want.URI = c.uri
		if got != c.want {
			t.Errorf("Parse(%q) = %+v, want %+v", c.uri, got, c.want)
		}
		if d := got.DriverURI(); d != c.driver {
			t.Errorf("Parse(%q).DriverURI() = %q, want %q", c.uri, d, c.driver)
		}
	}
	if a := (Target{Host: "::1"}).Addr(); a != "[::1]:22" {
		t.Errorf("Addr() = %q", a)
	}
}

func TestParseRejects(t *testing.T) {
	for _, uri := range []string{
		"",
		"/system",
		"qemu://",
		"qemu://kvm1/system",     // a host without a transport
		"qemu+ssh:///system",     // ssh without a host
		"qemu+tcp://kvm1/system", // not yet
		"qemu+ssh://h/system?proxy=nc",
	} {
		if _, err := Parse(uri); err == nil {
			t.Errorf("Parse(%q) accepted", uri)
		}
	}
}
