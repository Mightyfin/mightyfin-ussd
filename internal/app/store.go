package app

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

type App struct {
	mu    sync.Mutex
	db    Database
	dir   string
	key   []byte
	token string
	lock  *os.File
}

func Open(dir string) (*App, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "demo.lock"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, errors.New("data directory is locked; stop the other demo, or remove demo.lock after confirming it has stopped")
	}
	a := &App{db: emptyDB(), dir: dir, token: id("demo"), lock: lock}
	ok := false
	defer func() {
		if !ok {
			a.Close()
		}
	}()
	a.key, err = os.ReadFile(filepath.Join(dir, "demo.key"))
	if os.IsNotExist(err) {
		a.key = make([]byte, 32)
		_, err = rand.Read(a.key)
		if err == nil {
			err = os.WriteFile(filepath.Join(dir, "demo.key"), a.key, 0600)
		}
	}
	if err != nil {
		return nil, err
	}
	if len(a.key) != 32 {
		return nil, errors.New("invalid local encryption key")
	}
	blob, err := os.ReadFile(filepath.Join(dir, "state.enc"))
	if err == nil {
		plain, e := a.decrypt(blob)
		if e != nil {
			return nil, e
		}
		if err = json.Unmarshal(plain, &a.db); err != nil {
			return nil, err
		}
		var version struct{ SchemaVersion int }
		if err = json.Unmarshal(plain, &version); err != nil {
			return nil, err
		}
		if version.SchemaVersion > 2 {
			return nil, errors.New("unsupported state version")
		}
		if version.SchemaVersion < 2 {
			// Retain business records; remove obsolete credential state and cached PIN screens.
			a.db.Sessions = map[string]*Session{}
			a.db.SchemaVersion = 2
			if err = a.save(); err != nil {
				return nil, err
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if a.db.Customers == nil || a.db.Sessions == nil || a.db.Applications == nil || a.db.Offers == nil || a.db.Loans == nil || a.db.Quotes == nil || a.db.Operations == nil || a.db.Cases == nil {
		return nil, errors.New("invalid state file")
	}
	ok = true
	return a, nil
}
func (a *App) Close() {
	if a.lock != nil {
		_ = a.lock.Close()
		_ = os.Remove(filepath.Join(a.dir, "demo.lock"))
		a.lock = nil
	}
}
func (a *App) crypt() (cipher.AEAD, error) {
	b, err := aes.NewCipher(a.key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}
func (a *App) decrypt(b []byte) ([]byte, error) {
	g, err := a.crypt()
	if err != nil {
		return nil, err
	}
	if len(b) < g.NonceSize() {
		return nil, errors.New("corrupt state file")
	}
	return g.Open(nil, b[:g.NonceSize()], b[g.NonceSize():], []byte("mightyfin-ussd-demo-v1"))
}
func (a *App) save() error {
	plain, err := json.Marshal(a.db)
	if err != nil {
		return err
	}
	g, err := a.crypt()
	if err != nil {
		return err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return err
	}
	blob := g.Seal(nonce, nonce, plain, []byte("mightyfin-ussd-demo-v1"))
	f, err := os.CreateTemp(a.dir, "state-*.tmp")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(blob)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(a.dir, "state.enc"))
}

// Change and persistence share one lock. A failed write rolls back the in-memory change.
func (a *App) transaction(fn func() error) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	before, err := json.Marshal(a.db)
	if err != nil {
		return err
	}
	if err = fn(); err == nil {
		err = a.save()
	}
	if err != nil {
		a.db = emptyDB()
		_ = json.Unmarshal(before, &a.db)
	}
	return err
}
func (a *App) fingerprint(v string) string {
	m := hmac.New(sha256.New, a.key)
	_, _ = m.Write([]byte(v))
	return hex.EncodeToString(m.Sum(nil))
}
