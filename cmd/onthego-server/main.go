package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/jbaehova/onthego/internal/controlplane"
)

func main() {
	bootstrap := os.Getenv("ONTHEGO_LOGIN_BOOTSTRAP_TOKEN")
	signing := os.Getenv("ONTHEGO_CONTROL_PLANE_SIGNING_KEY")
	if bootstrap == "" || len(signing) < 32 {
		log.Fatal("ONTHEGO login bootstrap token and signing key are required")
	}
	listen := value("ONTHEGO_CONTROL_PLANE_LISTEN", ":443")
	dataDir := value("ONTHEGO_CONTROL_PLANE_DATA", "/var/lib/onthego")
	certificate := value("ONTHEGO_CONTROL_PLANE_TLS_CERT", "/var/lib/onthego/tls.crt")
	privateKey := value("ONTHEGO_CONTROL_PLANE_TLS_KEY", "/var/lib/onthego/tls.key")
	server := &http.Server{
		Addr:              listen,
		Handler:           (&controlplane.Server{BootstrapToken: bootstrap, SigningKey: []byte(signing), DataDir: dataDir}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	fmt.Printf("ONTHEGO control plane listening on %s\n", listen)
	if err := server.ListenAndServeTLS(certificate, privateKey); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func value(key, fallback string) string {
	if current := os.Getenv(key); current != "" {
		return current
	}
	return fallback
}
