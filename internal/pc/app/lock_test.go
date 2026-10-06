package app

import "testing"

func TestStateAllowsOneServerInstance(t *testing.T) {
	state := t.TempDir()
	one, e := lockState(state)
	if e != nil {
		t.Fatal(e)
	}
	if two, e := lockState(state); e == nil {
		two.Close()
		one.Close()
		t.Fatal("second server acquired state lock")
	}
	one.Close()
	two, e := lockState(state)
	if e != nil {
		t.Fatal(e)
	}
	two.Close()
}
