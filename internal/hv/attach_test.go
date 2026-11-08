package hv

import "testing"

func TestDeviceXML(t *testing.T) {
	for x, want := range map[string]string{
		"<hostdev mode='subsystem' type='usb'><source><vendor id='0x1'/><product id='0x2'/></source></hostdev>": "hostdev (usb)",
		"\n <watchdog model='i6300esb'/>\n":                               "watchdog (i6300esb)",
		"<filesystem type='mount'><driver type='virtiofs'/></filesystem>": "filesystem (mount)",
		"<panic/>": "panic",
	} {
		_, what, err := deviceXML(x)
		if err != nil || what != want {
			t.Errorf("%q: %q, %v; want %q", x, what, err, want)
		}
	}
	for _, bad := range []string{"", "  ", "<disk>", "<rng/><rng/>", "<domain type='kvm'/>", "<devices><rng/></devices>", "rng"} {
		if _, _, err := deviceXML(bad); err == nil {
			t.Errorf("%q: accepted", bad)
		}
	}
}
