package nyali

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"
)

type Entry struct {
	Ref               string    `json:"external_reference"`
	BorrowerHash      string    `json:"borrower_hash"`
	ClientID          int64     `json:"client_id"`
	LoanID            int64     `json:"loan_id"`
	Payer             string    `json:"payer"`
	Amount            string    `json:"amount"`
	Status            string    `json:"status"`
	Posted            bool      `json:"posted"`
	ProviderReference string    `json:"provider_reference"`
	Created           time.Time `json:"created"`
	Updated           time.Time `json:"updated"`
	ErrorCode         string    `json:"error_code,omitempty"`
}

func (e Entry) Open() bool { return e.Status != "Failed" && !(e.Status == "Successful" && e.Posted) }
func (e Entry) ShortRef() string {
	if len(e.Ref) > 17 {
		return e.Ref[:17]
	}
	return e.Ref
}
func Caller(phone string) string {
	if normalized, e := Phone(phone); e == nil {
		phone = normalized
	}
	s := sha256.Sum256([]byte("nyali-borrower-v1\x00" + phone))
	return hex.EncodeToString(s[:])
}

type Store struct {
	mu             sync.Mutex
	path           string
	box            cipher.AEAD
	lock           *os.File
	entries        map[string]Entry
	failed, closed bool
}

func OpenStore(path string, key []byte) (*Store, error) {
	if len(key) != 32 {
		return nil, errors.New("Nyali journal requires a 32-byte key")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	for _, p := range []string{path, path + ".lock"} {
		if info, e := os.Lstat(p); e == nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0) {
			return nil, errors.New("private regular journal files required")
		}
	}
	lock, e := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		lock.Close()
		return nil, errors.New("Nyali journal is already in use")
	}
	b, e := aes.NewCipher(key)
	if e != nil {
		lock.Close()
		return nil, e
	}
	box, e := cipher.NewGCM(b)
	if e != nil {
		lock.Close()
		return nil, e
	}
	s := &Store{path: path, box: box, lock: lock, entries: map[string]Entry{}}
	raw, e := os.ReadFile(path)
	if os.IsNotExist(e) {
		return s, nil
	}
	if e != nil {
		s.Close()
		return nil, e
	}
	if len(raw) < box.NonceSize() {
		s.Close()
		return nil, errors.New("invalid Nyali journal")
	}
	plain, e := box.Open(nil, raw[:box.NonceSize()], raw[box.NonceSize():], nil)
	if e != nil {
		s.Close()
		return nil, errors.New("Nyali journal decryption failed")
	}
	if e = json.Unmarshal(plain, &s.entries); e != nil {
		s.Close()
		return nil, e
	}
	if s.entries == nil {
		s.Close()
		return nil, errors.New("invalid Nyali journal contents")
	}
	return s, nil
}
func (s *Store) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
		s.lock.Close()
	}
}
func (s *Store) Healthy() bool { s.mu.Lock(); defer s.mu.Unlock(); return !s.failed && !s.closed }
func (s *Store) save() (err error) {
	defer func() {
		if err != nil {
			s.failed = true
		}
	}()
	if s.failed || s.closed {
		return errors.New("Nyali journal unavailable")
	}
	plain, e := json.Marshal(s.entries)
	if e != nil {
		return e
	}
	nonce := make([]byte, s.box.NonceSize())
	if _, e = rand.Read(nonce); e != nil {
		return e
	}
	raw := s.box.Seal(nonce, nonce, plain, nil)
	f, e := os.CreateTemp(filepath.Dir(s.path), ".nyali-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(raw)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	if e = os.Rename(f.Name(), s.path); e != nil {
		return e
	}
	d, e := os.Open(filepath.Dir(s.path))
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
func (s *Store) Reserve(want Entry) (Entry, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed || s.closed {
		return Entry{}, false, errors.New("Nyali journal unavailable")
	}
	if old, ok := s.entries[want.Ref]; ok {
		if old.BorrowerHash != want.BorrowerHash || old.LoanID != want.LoanID || old.Payer != want.Payer || old.Amount != want.Amount || old.ClientID != want.ClientID {
			return Entry{}, false, errors.New("Nyali request conflict")
		}
		return old, false, nil
	}
	for _, old := range s.entries {
		if old.LoanID == want.LoanID && old.Open() {
			return old, false, nil
		}
	}
	if len(s.entries) >= 50000 {
		return Entry{}, false, errors.New("Nyali journal capacity reached")
	}
	want.Status = "Submitting"
	want.Created = time.Now().UTC()
	want.Updated = want.Created
	s.entries[want.Ref] = want
	if e := s.save(); e != nil {
		return Entry{}, false, e
	}
	return want, true, nil
}
func (s *Store) Update(e Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	prior, ok := s.entries[e.Ref]
	if !ok {
		return errors.New("Nyali intent missing")
	}
	if prior.Status == "Successful" && prior.Posted {
		return nil
	}
	if prior.Status == "Successful" && e.Status != "Successful" {
		prior.ErrorCode = "PROVIDER_STATUS_CONFLICT"
		prior.Updated = time.Now().UTC()
		s.entries[e.Ref] = prior
		return s.save()
	}
	if prior.Status == "Failed" && (e.Status == "Pending" || e.Status == "Submitting" || e.Status == "Unknown") {
		return nil
	}
	e.Updated = time.Now().UTC()
	s.entries[e.Ref] = e
	return s.save()
}
func (s *Store) Snapshot() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Entry, 0, len(s.entries))
	for _, e := range s.entries {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}
func (s *Store) Latest(caller string, loan int64) (Entry, bool) {
	all := s.Snapshot()
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].BorrowerHash == Caller(caller) && all[i].LoanID == loan {
			return all[i], true
		}
	}
	return Entry{}, false
}

// Lookup returns a previously reserved operation without changing its state.
func (s *Store) Lookup(reference string) (Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[reference]
	return entry, ok
}
