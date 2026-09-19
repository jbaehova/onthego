package controlplane

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPinnedCertificateValidity(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name                string
		notBefore, notAfter time.Time
		wrongPin            bool
		wantError           string
	}{
		{"valid", now.Add(-time.Hour), now.Add(time.Hour), false, ""},
		{"wrong pin", now.Add(-time.Hour), now.Add(time.Hour), true, "fingerprint mismatch"},
		{"expired", now.Add(-2 * time.Hour), now.Add(-time.Hour), false, "expired or not yet valid"},
		{"future", now.Add(time.Hour), now.Add(2 * time.Hour), false, "expired or not yet valid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: tc.notBefore, NotAfter: tc.notAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
			der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
			server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
			server.StartTLS()
			defer server.Close()
			fingerprint := sha256.Sum256(der)
			if tc.wrongPin {
				fingerprint[0] ^= 0xff
			}
			client, err := (Client{BaseURL: server.URL, TLSFingerprint: hex.EncodeToString(fingerprint[:])}).httpClient()
			if err != nil {
				t.Fatal(err)
			}
			defer client.CloseIdleConnections()
			response, err := client.Get(server.URL + "/v1/health")
			if response != nil {
				response.Body.Close()
			}
			if tc.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("wanted %q, got %v", tc.wantError, err)
			}
		})
	}
}
