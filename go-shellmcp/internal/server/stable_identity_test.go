package server

import "testing"

func TestPersistedIdentityKeepsQueueAcrossHostnameChange(t *testing.T) {
	dir := t.TempDir()
	a := New(Config{Name: "mac-original", IdentityDir: dir, SpillDir: t.TempDir()})
	oldName, oldID, err := a.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	a.Close()
	b := New(Config{Name: "mac-renamed", IdentityDir: dir, SpillDir: t.TempDir()})
	defer b.Close()
	if b.newBeat().Hostname != "mac-renamed" {
		t.Fatal("display hostname did not update")
	}
	name, id, err := b.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if name != oldName || b.cfg.Name != oldName || id["server_id"] != oldID["server_id"] || id["public_key"] != oldID["public_key"] {
		t.Fatalf("identity/queue changed: %s -> %s (queue %s)", oldName, name, b.cfg.Name)
	}
}
