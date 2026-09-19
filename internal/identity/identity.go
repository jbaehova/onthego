package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"filippo.io/age"
	"github.com/jbaehova/onthego/internal/config"
)

type Keys struct {
	AgeIdentity    string `json:"age_identity"`
	AgeRecipient   string `json:"age_recipient"`
	SigningPrivate string `json:"signing_private"`
	SigningPublic  string `json:"signing_public"`
}

func LoadOrCreate() (Keys, error) {
	base, err := config.DataRoot()
	if err != nil {
		return Keys{}, err
	}
	dir := filepath.Join(base, "identity")
	path := filepath.Join(dir, "keys.json")
	data, err := os.ReadFile(path)
	if err == nil {
		var keys Keys
		if err := json.Unmarshal(data, &keys); err != nil {
			return Keys{}, fmt.Errorf("parse identity: %w", err)
		}
		return keys, nil
	}
	if !os.IsNotExist(err) {
		return Keys{}, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Keys{}, err
	}
	ageID, err := age.GenerateX25519Identity()
	if err != nil {
		return Keys{}, err
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Keys{}, err
	}
	keys := Keys{
		AgeIdentity:    ageID.String(),
		AgeRecipient:   ageID.Recipient().String(),
		SigningPrivate: base64.StdEncoding.EncodeToString(priv),
		SigningPublic:  base64.StdEncoding.EncodeToString(pub),
	}
	data, err = json.MarshalIndent(keys, "", "  ")
	if err != nil {
		return Keys{}, err
	}
	tmp, err := os.CreateTemp(dir, "keys-*.tmp")
	if err != nil {
		return Keys{}, err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return Keys{}, err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return Keys{}, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return Keys{}, err
	}
	if err := tmp.Close(); err != nil {
		return Keys{}, err
	}
	if err := os.Rename(name, path); err != nil {
		return Keys{}, err
	}
	return keys, nil
}

func (k Keys) Recipient() (age.Recipient, error) {
	return age.ParseX25519Recipient(k.AgeRecipient)
}

func (k Keys) Identity() (age.Identity, error) {
	return age.ParseX25519Identity(k.AgeIdentity)
}

func (k Keys) PrivateKey() (ed25519.PrivateKey, error) {
	b, err := base64.StdEncoding.DecodeString(k.SigningPrivate)
	if err != nil || len(b) != ed25519.PrivateKeySize {
		return nil, errorsJoin("invalid signing private key", err)
	}
	return ed25519.PrivateKey(b), nil
}

func ParsePublic(encoded string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errorsJoin("invalid signing public key", err)
	}
	return ed25519.PublicKey(b), nil
}

func errorsJoin(message string, err error) error {
	if err == nil {
		return fmt.Errorf("%s", message)
	}
	return fmt.Errorf("%s: %w", message, err)
}
