package nyali

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestJournalDurabilityIsolationAndPendingGuard(t *testing.T) {
	path := filepath.Join(t.TempDir(), "payments.enc")
	key := bytes.Repeat([]byte{7}, 32)
	s, e := OpenStore(path, key)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = OpenStore(path, key); e == nil {
		t.Fatal("second writer allowed")
	}
	want := Entry{Ref: "USSD-" + strings.Repeat("a", 64), BorrowerHash: Caller("260970000001"), ClientID: 3, LoanID: 42, Payer: "260970000002", Amount: "25.00"}
	r, fresh, e := s.Reserve(want)
	if e != nil || !fresh {
		t.Fatal(r, fresh, e)
	}
	raw, _ := os.ReadFile(path)
	if bytes.Contains(raw, []byte(want.Payer)) || bytes.Contains(raw, []byte(want.Ref)) {
		t.Fatal("journal is plaintext")
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	s.Close()
	s, e = OpenStore(path, key)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	r, fresh, e = s.Reserve(want)
	if e != nil || fresh || r.Ref != want.Ref {
		t.Fatal("intent lost after restart", e)
	}
	changed := want
	changed.Amount = "26.00"
	if _, _, e = s.Reserve(changed); e == nil {
		t.Fatal("reference conflict allowed")
	}
	changed = want
	changed.Ref = "USSD-" + strings.Repeat("b", 64)
	r, fresh, e = s.Reserve(changed)
	if e != nil || fresh || r.Ref != want.Ref {
		t.Fatal("pending duplicate allowed", e)
	}
	r.Status = "Successful"
	r.Posted = true
	if e = s.Update(r); e != nil {
		t.Fatal(e)
	}
	r.Status = "Pending"
	r.Posted = false
	s.Update(r)
	latest, _ := s.Latest("0970000001", 42)
	if latest.Status != "Successful" || !latest.Posted {
		t.Fatal("posted success regressed")
	}
	if _, ok := s.Latest("260970000099", 42); ok {
		t.Fatal("caller isolation failed")
	}
}
func TestConcurrentJournalReservations(t *testing.T) {
	s, e := OpenStore(filepath.Join(t.TempDir(), "payments.enc"), bytes.Repeat([]byte{8}, 32))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	var wg sync.WaitGroup
	var mu sync.Mutex
	freshCount := 0
	for _, key := range []string{"a", "b"} {
		wg.Add(1)
		go func(k string) {
			defer wg.Done()
			_, fresh, e := s.Reserve(Entry{Ref: "USSD-" + strings.Repeat(k, 64), LoanID: 42, BorrowerHash: Caller("260970000001"), ClientID: 3, Payer: "260970000001", Amount: "1.00"})
			if e != nil {
				t.Error(e)
			}
			if fresh {
				mu.Lock()
				freshCount++
				mu.Unlock()
			}
		}(key)
	}
	wg.Wait()
	if freshCount != 1 {
		t.Fatal(freshCount)
	}
}
func TestJournalFailureAndUnpostedFinality(t *testing.T) {
	path := filepath.Join(t.TempDir(), "payments.enc")
	key := bytes.Repeat([]byte{9}, 32)
	s, e := OpenStore(path, key)
	if e != nil {
		t.Fatal(e)
	}
	r, _, e := s.Reserve(Entry{Ref: "USSD-fixture", LoanID: 42})
	if e != nil {
		t.Fatal(e)
	}
	r.Status = "Successful"
	r.Posted = false
	s.Update(r)
	r.Status = "Failed"
	s.Update(r)
	stored := s.Snapshot()[0]
	if stored.Status != "Successful" || !stored.Open() || stored.ErrorCode != "PROVIDER_STATUS_CONFLICT" {
		t.Fatal(stored)
	}
	s.Close()
	if _, e = OpenStore(path, bytes.Repeat([]byte{1}, 32)); e == nil {
		t.Fatal("wrong key accepted")
	}
}
