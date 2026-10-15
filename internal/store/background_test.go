package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestCloseAwaitsBackgroundWork(t *testing.T) {
	s, err := Open(t.Context(), filepath.Join(t.TempDir(), "tm.db"))
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	finished := make(chan struct{})
	s.Go("test:bg", "", func() {
		close(started)
		time.Sleep(100 * time.Millisecond)
		if _, err := s.DB.Exec(`SELECT 1`); err != nil {
			t.Errorf("background work saw a closed store: %v", err)
		}
		close(finished)
	})
	<-started
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	default:
		t.Fatal("Close returned while background work was still running")
	}

	ran := make(chan struct{}, 1)
	s.Go("test:late", "", func() { ran <- struct{}{} })
	select {
	case <-ran:
		t.Fatal("Go ran work on a closed store")
	case <-time.After(50 * time.Millisecond):
	}
}
