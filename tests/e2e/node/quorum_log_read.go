// Read an inactive CI voter's Raft commands using the same Bolt and Raft codecs as Node.
// The first two uint64s are the retained Bolt bounds, including non-command entries.
// Stdout is a private pipe to quorum_storage.py. Never send its contents to a CI log.
package main

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/hashicorp/raft"
	raftboltdb "github.com/hashicorp/raft-boltdb/v2"
	"go.etcd.io/bbolt"
)

func readCommands(path string, out io.Writer) error {
	store, err := raftboltdb.New(raftboltdb.Options{
		Path: path, BoltOptions: &bbolt.Options{ReadOnly: true, Timeout: 2 * time.Second},
	})
	if err != nil {
		return err
	}
	defer store.Close()
	first, err := store.FirstIndex()
	if err != nil {
		return err
	}
	last, err := store.LastIndex()
	if err != nil || first == 0 || last < first {
		return errors.New("missing log range")
	}
	w := bufio.NewWriter(out)
	if err := binary.Write(w, binary.BigEndian, [2]uint64{first, last}); err != nil {
		return err
	}
	for i := first; i <= last; i++ {
		var entry raft.Log
		if err := store.GetLog(i, &entry); err != nil {
			return err
		}
		if entry.Type != raft.LogCommand {
			continue
		}
		if _, err := w.Write(binary.BigEndian.AppendUint64(nil, i)); err != nil {
			return err
		}
		if _, err := w.Write(binary.BigEndian.AppendUint64(nil, uint64(len(entry.Data)))); err != nil {
			return err
		}
		if _, err := w.Write(entry.Data); err != nil {
			return err
		}
	}
	return w.Flush()
}

func main() {
	if len(os.Args) != 2 || readCommands(os.Args[1], os.Stdout) != nil {
		fmt.Fprintln(os.Stderr, "could not inspect the isolated voter's Raft store")
		os.Exit(1)
	}
}
