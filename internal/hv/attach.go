package hv

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/jing2uo/virt-meow/internal/conn"
)

// rawDevice is a device's XML as the user wrote it, for edit.attach.
type rawDevice string

func (r rawDevice) Marshal() (string, error) { return string(r), nil }

// deviceXML checks that x is one device element and names it for the
// output: its element and type, as "hostdev (usb)".
func deviceXML(x string) (rawDevice, string, error) {
	x = strings.TrimSpace(x)
	if x == "" {
		return "", "", errors.New("no device XML")
	}
	n, err := parseTree(x)
	if err != nil {
		return "", "", fmt.Errorf("not XML: %w", err)
	}
	if roots, err := topElements(x); err != nil {
		return "", "", fmt.Errorf("not XML: %w", err)
	} else if roots != 1 {
		return "", "", fmt.Errorf("want one device element, found %d: attach them one at a time", roots)
	}
	switch n.name {
	case "domain", "devices":
		return "", "", fmt.Errorf("want one device element (<disk>, <hostdev>, <filesystem>, …), not <%s>", n.name)
	}
	what := n.name
	for _, a := range []string{"type", "model"} {
		if v := n.attrs[a]; v != "" {
			what += " (" + v + ")"
			break
		}
	}
	return rawDevice(x), what, nil
}

// AttachDevice adds the device x (one element of <devices>, as virsh
// attach-device takes) to the machine's definition, and to the running
// machine where it can take it; the outcome says which.
func AttachDevice(ctx context.Context, sess *conn.Session, uuid, x string) (Edited, error) {
	dev, what, err := deviceXML(x)
	if err != nil {
		return Edited{}, err
	}
	return modify(ctx, sess, uuid, func(e *edit) error { return e.attach(what, dev) })
}

// DetachDevice removes the device x describes from the machine's
// definition, and asks the running machine to let go of it.
func DetachDevice(ctx context.Context, sess *conn.Session, uuid, x string) (Edited, error) {
	dev, what, err := deviceXML(x)
	if err != nil {
		return Edited{}, err
	}
	return modify(ctx, sess, uuid, func(e *edit) error { return e.detach(what, dev) })
}

// topElements counts the top-level elements of x.
func topElements(x string) (int, error) {
	dec := xml.NewDecoder(strings.NewReader(x))
	depth, n := 0, 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return n, nil
		}
		if err != nil {
			return n, err
		}
		switch tok.(type) {
		case xml.StartElement:
			if depth == 0 {
				n++
			}
			depth++
		case xml.EndElement:
			depth--
		}
	}
}
