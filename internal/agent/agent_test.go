package agent

import "testing"

func TestPermissionHelpers(t *testing.T) {
	p := Permission{Options: DefaultOptions()}
	if o, ok := p.Find("deny"); !ok || o.Kind != RejectOnce {
		t.Errorf("find deny = %+v %v", o, ok)
	}
	if _, ok := p.Find("missing"); ok {
		t.Error("found a missing option")
	}
	if o, ok := p.First(OptionKind.Allows); !ok || o.ID != "allow" {
		t.Errorf("first allow = %+v", o)
	}
	if _, ok := (Permission{}).First(OptionKind.Allows); ok {
		t.Error("first on no options")
	}
}

func TestOptionKinds(t *testing.T) {
	for k, want := range map[OptionKind]bool{AllowOnce: true, AllowAlways: true, RejectOnce: false, RejectAlways: false} {
		if k.Allows() != want {
			t.Errorf("%s.Allows() = %v", k, k.Allows())
		}
	}
}

func TestCapsAndBlocks(t *testing.T) {
	c := Caps{Modes: []Mode{ModeAsk, ModePlan}}
	if !c.SupportsMode(ModePlan) || c.SupportsMode(ModeFull) {
		t.Errorf("SupportsMode wrong for %v", c.Modes)
	}
	if (Block{Text: "x"}).IsImage() || !(Block{Image: []byte{1}}).IsImage() {
		t.Error("IsImage")
	}
	if (NoOptions{}).Decode(&struct{}{}) != nil {
		t.Error("NoOptions.Decode failed")
	}
}
