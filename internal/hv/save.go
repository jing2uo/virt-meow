package hv

import (
	"context"
	"fmt"

	libvirt "github.com/digitalocean/go-libvirt"

	"github.com/jing2uo/virt-meow/internal/conn"
)

// Save stops a running machine with its memory saved on the host (virsh
// managedsave): the next start resumes it where it was. A paused machine
// stays paused when it resumes.
func Save(ctx context.Context, sess *conn.Session, uuid string) error {
	u, err := ParseUUID(uuid)
	if err != nil {
		return err
	}
	return sess.Do(ctx, func(lv *libvirt.Libvirt) error {
		dom, err := lv.DomainLookupByUUID(u)
		if err != nil {
			return err
		}
		if active, err := lv.DomainIsActive(dom); err != nil {
			return err
		} else if active == 0 {
			return fmt.Errorf("%s is not running: there is nothing to save", dom.Name)
		}
		return vdagentHint(lv.DomainManagedSave(dom, 0), dom.Name)
	})
}

// DropSave removes a machine's saved state (virsh managedsave-remove), so
// that its next start boots afresh. It reports whether there was one.
func DropSave(ctx context.Context, sess *conn.Session, uuid string) (bool, error) {
	u, err := ParseUUID(uuid)
	if err != nil {
		return false, err
	}
	var had bool
	err = sess.Do(ctx, func(lv *libvirt.Libvirt) error {
		dom, err := lv.DomainLookupByUUID(u)
		if err != nil {
			return err
		}
		h, err := lv.DomainHasManagedSaveImage(dom, 0)
		if err != nil || h == 0 {
			return err
		}
		had = true
		return lv.DomainManagedSaveRemove(dom, 0)
	})
	return had, err
}
