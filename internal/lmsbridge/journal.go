package lmsbridge

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

type entry struct {
	Fingerprint string  `json:"fingerprint"`
	Result      *Result `json:"result,omitempty"`
}

// Journal records an intent before contacting LMS. An unresolved intent must be
// reconciled, never automatically resent after a timeout or process crash.
type Journal struct {
	mu      sync.Mutex
	path    string
	cipher  cipher.AEAD
	entries map[string]entry
}

func OpenJournal(path string, key []byte) (*Journal, error) {
	if len(key) != 32 {
		return nil, errors.New("journal key must contain 32 bytes")
	}
	block, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	a, e := cipher.NewGCM(block)
	if e != nil {
		return nil, e
	}
	j := &Journal{path: path, cipher: a, entries: map[string]entry{}}
	raw, e := os.ReadFile(path)
	if os.IsNotExist(e) {
		return j, nil
	}
	if e != nil {
		return nil, e
	}
	if len(raw) < a.NonceSize() {
		return nil, errors.New("invalid journal")
	}
	plain, e := a.Open(nil, raw[:a.NonceSize()], raw[a.NonceSize():], nil)
	if e != nil {
		return nil, errors.New("journal decryption failed")
	}
	if e = json.Unmarshal(plain, &j.entries); e != nil {
		return nil, e
	}
	return j, nil
}
func (j *Journal) save() error {
	plain, e := json.Marshal(j.entries)
	if e != nil {
		return e
	}
	nonce := make([]byte, j.cipher.NonceSize())
	if _, e = rand.Read(nonce); e != nil {
		return e
	}
	raw := j.cipher.Seal(nonce, nonce, plain, nil)
	f, e := os.CreateTemp(filepath.Dir(j.path), ".journal-*")
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
	closeErr := f.Close()
	if e == nil {
		e = closeErr
	}
	if e != nil {
		return e
	}
	if e = os.Rename(f.Name(), j.path); e != nil {
		return e
	}
	d, e := os.Open(filepath.Dir(j.path))
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
func (j *Journal) Do(key, fp string, send func() (Result, error)) (Result, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if prior, ok := j.entries[key]; ok {
		if prior.Fingerprint != fp {
			return Result{409, json.RawMessage(`{"error":"REQUEST_KEY_CONFLICT"}`)}, nil
		}
		if prior.Result == nil {
			return Result{409, json.RawMessage(`{"error":"RECONCILIATION_REQUIRED","message":"Previous request outcome is unresolved. Check LMS before trying again."}`)}, nil
		}
		return *prior.Result, nil
	}
	if len(j.entries) >= 10000 {
		return Result{}, errors.New("journal capacity reached")
	}
	j.entries[key] = entry{Fingerprint: fp}
	if e := j.save(); e != nil {
		return Result{}, e
	} // Fail closed; keep uncertain reservation.
	r, e := send()
	if e != nil {
		return Result{}, e
	}
	if r.Status == 408 || r.Status == 429 {
		return Result{}, errors.New("LMS acceptance requires reconciliation")
	}
	j.entries[key] = entry{Fingerprint: fp, Result: &r}
	if e = j.save(); e != nil {
		j.entries[key] = entry{Fingerprint: fp}
		return Result{}, e
	}
	return r, nil
}
