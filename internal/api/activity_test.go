package api

import (
	"strings"
	"testing"
)

func TestActivityLog(t *testing.T) {
	var l activityLog
	for i := 0; i < activityKeep+10; i++ {
		l.add(Activity{Kind: "event", What: "started"})
	}
	got := l.list()
	if len(got) != activityKeep || got[0].ID != activityKeep+10 || got[len(got)-1].ID != 11 {
		t.Fatalf("%d entries, %d…%d", len(got), got[0].ID, got[len(got)-1].ID)
	}
}

func TestRecordActionClips(t *testing.T) {
	s := &Service{c: &Core{}}
	a, err := s.RecordAction(Activity{Kind: "event", What: "Set", Detail: strings.Repeat("长", 300)})
	if err != nil || a.Kind != "action" || len([]rune(a.Detail)) != 85 {
		t.Fatalf("%+v %v (%d runes)", a, err, len([]rune(a.Detail)))
	}
}
