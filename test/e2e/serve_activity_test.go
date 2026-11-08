//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/jing2uo/virt-meow/internal/api"
	"github.com/jing2uo/virt-meow/internal/hv"
)

// The activity log under virt-mm serve: a machine's events (started from
// elsewhere, here hv), an action a page reports, and an upload that ended,
// newest first, and every new entry sent to every page (a second one
// here, its own event stream).
func TestServeActivity(t *testing.T) {
	s := qemuSession(t)
	scratchPool(t, s, "e2e-act")
	sv := serve(t)
	c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	other, _, err := websocket.Dial(c, "ws://"+sv.listen+"/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer other.CloseNow()

	cr, err := hv.Create(ctx(t), s, hv.Spec{Name: "e2e-act", OS: "linux", VCPUs: 1, MemoryMiB: 256,
		DiskPool: "e2e-act", DiskGiB: 1, NetKind: "none", Firmware: "bios"}, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hv.Delete(ctx(t), s, cr.Domain.UUID, true) })
	if err := hv.Power(ctx(t), s, cr.Domain.UUID, hv.Start); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hv.Power(ctx(t), s, cr.Domain.UUID, hv.ForceOff) })
	isEntry := func(kind, what string) func(json.RawMessage) bool {
		return func(d json.RawMessage) bool {
			var a api.Activity
			json.Unmarshal(d, &a)
			return a.Kind == kind && a.What == what && (kind != "event" || a.Machine == "e2e-act" && a.Connection == sv.conn)
		}
	}
	await(t, other, "activity", isEntry("event", "started"))

	var a api.Activity
	sv.call(&a, "RecordAction", map[string]any{"what": "Power", "connection": sv.conn, "machine": "e2e-act", "detail": "start"})
	if a.ID == 0 || a.Kind != "action" || a.Time.IsZero() {
		t.Errorf("RecordAction: %+v", a)
	}
	await(t, other, "activity", isEntry("action", "Power"))
	if err := sv.try(nil, "RecordAction", map[string]any{"connection": sv.conn}); err == nil {
		t.Error("an action that names nothing")
	}

	data := bytes.Repeat([]byte("x"), 100_000)
	req, _ := http.NewRequest(http.MethodPost, sv.base+"/api/upload?connection="+sv.conn+"&pool=e2e-act&name=act.iso", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/octet-stream")
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("upload: %v %v", res, err)
	}
	res.Body.Close()
	await(t, other, "activity", isEntry("upload", "act.iso"))

	var log []api.Activity
	sv.call(&log, "Activity")
	if len(log) < 3 {
		t.Fatalf("Activity: %+v", log)
	}
	for i := 1; i < len(log); i++ {
		if log[i].ID >= log[i-1].ID {
			t.Errorf("not newest first: %d after %d", log[i].ID, log[i-1].ID)
		}
	}
	if log[0].Kind != "upload" || log[0].Detail != "done → e2e-act" {
		t.Errorf("newest: %+v", log[0])
	}
}
