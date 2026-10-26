package app

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLegacyPINStateMigratesWithoutLosingApplication(t *testing.T) {
	a := testApp(t)
	_, applicationID := application(t, a)
	b, err := json.Marshal(a.db)
	if err != nil {
		t.Fatal(err)
	}
	var legacy map[string]any
	if err = json.Unmarshal(b, &legacy); err != nil {
		t.Fatal(err)
	}
	delete(legacy, "SchemaVersion")
	for _, value := range legacy["Customers"].(map[string]any) {
		c := value.(map[string]any)
		c["pin_hash"] = "legacy-secret-hash"
		c["salt"] = "legacy-salt"
		c["locked"] = true
	}
	legacy["Sessions"] = map[string]any{"old-pin-session": map[string]any{"Screen": "register_pin", "RegistrationHash": "legacy-registration-hash"}}
	b, err = json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	g, err := a.crypt()
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, g.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	blob := g.Seal(nonce, nonce, b, []byte("mightyfin-ussd-demo-v1"))
	if err = os.WriteFile(filepath.Join(a.dir, "state.enc"), blob, 0600); err != nil {
		t.Fatal(err)
	}
	dir := a.dir
	a.Close()
	restored, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if restored.db.Applications[applicationID] == nil || restored.db.Applications[applicationID].Status != "KYC_PENDING" {
		t.Fatal("migration lost business record")
	}
	if len(restored.db.Sessions) != 0 {
		t.Fatal("obsolete PIN sessions survived migration")
	}
	saved, err := os.ReadFile(filepath.Join(dir, "state.enc"))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := restored.decrypt(saved)
	if err != nil {
		t.Fatal(err)
	}
	for _, obsolete := range []string{"pin_hash", "legacy-secret-hash", "legacy-salt", "legacy-registration-hash", "register_pin"} {
		if bytes.Contains(plain, []byte(obsolete)) {
			t.Fatalf("migration retained %s", obsolete)
		}
	}
	h := dial(t, restored)
	h.send("1")
	h.has("Welcome to MightyFin, Demo Customer")
}
