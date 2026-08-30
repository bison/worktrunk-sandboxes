package state

import (
	"sync"
	"testing"
	"time"
)

func TestRoundTrip(t *testing.T) {
	s := New(t.TempDir())
	rows, err := s.Load()
	if err != nil || rows != nil {
		t.Fatalf("empty load = %v, %v", rows, err)
	}
	g := Grant{Expiry: 1234, Sandbox: "sb", Branch: "b", Repo: "/r", Name: "n", Undo: "service:github"}
	if err := s.Update(func(rows []Grant) []Grant { return append(rows, g) }); err != nil {
		t.Fatal(err)
	}
	rows, err = s.Load()
	if err != nil || len(rows) != 1 || rows[0] != g {
		t.Fatalf("load = %+v, %v", rows, err)
	}
}

func TestConcurrentUpdatesLoseNothing(t *testing.T) {
	s := New(t.TempDir())
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				err := s.Update(func(rows []Grant) []Grant {
					return append(rows, Grant{Name: "g", Sandbox: "sb"})
				})
				if err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	rows, err := s.Load()
	if err != nil || len(rows) != 40 {
		t.Fatalf("got %d rows, want 40 (err=%v)", len(rows), err)
	}
}

func TestExpired(t *testing.T) {
	now := time.Unix(1000, 0)
	if (Grant{Expiry: 0}).Expired(now) {
		t.Error("no-expiry grant reported expired")
	}
	if (Grant{Expiry: 1001}).Expired(now) {
		t.Error("future grant reported expired")
	}
	if !(Grant{Expiry: 1000}).Expired(now) {
		t.Error("due grant not reported expired")
	}
}
