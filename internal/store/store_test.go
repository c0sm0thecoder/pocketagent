package store

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPersistsAcrossReopen(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	st, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Update("c1", func(c *Conversation) { c.Agent, c.SessionID = "a", "s1" }); err != nil {
		t.Fatal(err)
	}
	if err := st.AddSpend("u", 1.5); err != nil {
		t.Fatal(err)
	}

	again, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if c := again.Get("c1"); c.Agent != "a" || c.SessionID != "s1" {
		t.Errorf("conversation = %+v", c)
	}
	if today, month := again.Spend("u"); today != 1.5 || month != 1.5 {
		t.Errorf("spend = %v %v", today, month)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Errorf("state file mode = %v", fi.Mode().Perm())
	}
}

// Get returns a copy: changing it must not change the store.
func TestGetReturnsACopy(t *testing.T) {
	st, _ := Open(filepath.Join(t.TempDir(), "s.json"))
	st.Update("c", func(c *Conversation) { c.AlwaysAllow = []string{"x"} })
	got := st.Get("c")
	got.AlwaysAllow[0] = "changed"
	if st.Get("c").AlwaysAllow[0] != "x" {
		t.Error("Get exposed internal state")
	}
	if empty := st.Get("unknown"); empty.Agent != "" {
		t.Errorf("unknown = %+v", empty)
	}
}

func TestRecentSessionsAreDedupedAndCapped(t *testing.T) {
	var c Conversation
	for i := range maxSessions + 5 {
		c.RecordSession(SessionRecord{ID: fmt.Sprint(i)})
	}
	c.RecordSession(SessionRecord{ID: "3", Title: "again"})
	if len(c.Sessions) != maxSessions || c.Sessions[0].ID != "3" || c.Sessions[0].Title != "again" {
		t.Errorf("sessions = %d, first %+v", len(c.Sessions), c.Sessions[0])
	}
	for _, s := range c.Sessions[1:] {
		if s.ID == "3" {
			t.Error("duplicate session")
		}
	}
}

func TestCheckpointsAreCapped(t *testing.T) {
	var c Conversation
	for i := range maxCheckpoints + 3 {
		c.PushCheckpoint(Checkpoint{Commit: fmt.Sprint(i)})
	}
	if len(c.Checkpoints) != maxCheckpoints || c.Checkpoints[len(c.Checkpoints)-1].Commit != fmt.Sprint(maxCheckpoints+2) {
		t.Errorf("checkpoints = %d", len(c.Checkpoints))
	}
}

func TestOldSpendIsDropped(t *testing.T) {
	st, _ := Open(filepath.Join(t.TempDir(), "s.json"))
	st.s.Spend["u"] = map[string]float64{day(time.Now().AddDate(0, 0, -40)): 9, day(time.Now().AddDate(0, 0, -3)): 2}
	st.AddSpend("u", 1)
	today, month := st.Spend("u")
	if today != 1 || month != 3 {
		t.Errorf("today %v month %v", today, month)
	}
	if len(st.s.Spend["u"]) != 2 {
		t.Errorf("old day kept: %v", st.s.Spend["u"])
	}
}

func TestCorruptStateIsAnError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.json")
	os.WriteFile(p, []byte("{not json"), 0o600)
	if _, err := Open(p); err == nil {
		t.Error("corrupt state accepted")
	}
}
