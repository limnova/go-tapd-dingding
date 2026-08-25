package crypto

import (
	"encoding/base64"
	"testing"
)

func TestEncryptDecrypt(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	t.Setenv(keyEnv, base64.RawStdEncoding.EncodeToString(key))
	box, err := FromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := box.Encrypt("tapd-secret")
	if err != nil {
		t.Fatal(err)
	}
	if encoded == "tapd-secret" || encoded == "" {
		t.Fatal("secret was not encrypted")
	}
	decoded, err := box.Decrypt(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != "tapd-secret" {
		t.Fatalf("unexpected plaintext: %q", decoded)
	}
	if _, err := box.Decrypt(encoded + "x"); err == nil {
		t.Fatal("tampered ciphertext was accepted")
	}
}
