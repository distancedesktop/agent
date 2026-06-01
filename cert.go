package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	certValidity = 13 * 24 * time.Hour
	rotateBefore = 7 * 24 * time.Hour
)

type certEntry struct {
	Cert        tls.Certificate
	Fingerprint []byte
	NotAfter    time.Time
}

type certManager struct {
	mu      sync.RWMutex
	entries []certEntry
	dir     string
}

func configDir() string {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, "distancedesktop")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "distancedesktop")
}

func newCertManager(dir string) (*certManager, error) {
	cm := &certManager{dir: dir}
	os.MkdirAll(dir, 0700)

	certPath := filepath.Join(dir, "ec-cert.pem")
	keyPath := filepath.Join(dir, "ec-key.pem")
	prevCertPath := filepath.Join(dir, "ec-cert.previous.pem")
	prevKeyPath := filepath.Join(dir, "ec-key.previous.pem")

	for _, pair := range [][2]string{{certPath, keyPath}, {prevCertPath, prevKeyPath}} {
		if _, err := os.Stat(pair[0]); err == nil {
			if _, err := os.Stat(pair[1]); err == nil {
				entry, err := loadCert(pair[0], pair[1])
				if err == nil && time.Now().Before(entry.NotAfter) {
					cm.entries = append(cm.entries, entry)
				}
			}
		}
	}

	if len(cm.entries) == 0 {
		entry, err := cm.generate()
		if err != nil {
			return nil, err
		}
		cm.entries = append(cm.entries, entry)
	}

	return cm, nil
}

func loadCert(certPath, keyPath string) (certEntry, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return certEntry{}, err
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return certEntry{}, fmt.Errorf("no PEM block in %s", certPath)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return certEntry{}, err
	}
	tlsCert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return certEntry{}, err
	}
	fp := sha256.Sum256(block.Bytes)
	return certEntry{
		Cert:        tlsCert,
		Fingerprint: fp[:],
		NotAfter:    cert.NotAfter,
	}, nil
}

func (cm *certManager) TLSConfig() *tls.Config {
	return &tls.Config{
		GetCertificate: cm.getCertificate,
		NextProtos:     []string{"h3"},
	}
}

func (cm *certManager) getCertificate(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	if len(cm.entries) == 0 {
		return nil, fmt.Errorf("no certificates available")
	}
	return &cm.entries[0].Cert, nil
}

func (cm *certManager) ActiveFingerprint() []byte {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	if len(cm.entries) == 0 {
		return nil
	}
	return cm.entries[0].Fingerprint
}

func (cm *certManager) NeedsRotation() bool {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	if len(cm.entries) == 0 {
		return true
	}
	return time.Now().Add(rotateBefore).After(cm.entries[0].NotAfter)
}

func (cm *certManager) FingerprintHex() string {
	fp := cm.ActiveFingerprint()
	if fp == nil {
		return ""
	}
	return fmt.Sprintf("%x", fp)
}

func (cm *certManager) Rotate() ([]byte, error) {
	entry, err := cm.generate()
	if err != nil {
		return nil, err
	}

	cm.mu.Lock()
	defer cm.mu.Unlock()

	certPath := filepath.Join(cm.dir, "ec-cert.pem")
	keyPath := filepath.Join(cm.dir, "ec-key.pem")
	prevCertPath := filepath.Join(cm.dir, "ec-cert.previous.pem")
	prevKeyPath := filepath.Join(cm.dir, "ec-key.previous.pem")

	if len(cm.entries) > 0 {
		os.Rename(certPath, prevCertPath)
		os.Rename(keyPath, prevKeyPath)
	}

	cm.entries = append([]certEntry{entry}, cm.entries...)
	cm.pruneLocked()

	return entry.Fingerprint, nil
}

func (cm *certManager) pruneLocked() {
	var keep []certEntry
	now := time.Now()
	for _, e := range cm.entries {
		if e.NotAfter.After(now) {
			keep = append(keep, e)
		} else {
			log.Printf("cert expired %v, dropping", e.NotAfter)
		}
	}
	cm.entries = keep
}

func (cm *certManager) generate() (certEntry, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return certEntry{}, fmt.Errorf("ecdsa key: %w", err)
	}

	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "localhost"
	}

	ips := []net.IP{{127, 0, 0, 1}, net.ParseIP("::1")}
	ifaces, err := net.Interfaces()
	if err == nil {
		for _, iface := range ifaces {
			addrs, err := iface.Addrs()
			if err != nil {
				continue
			}
			for _, a := range addrs {
				if ipnet, ok := a.(*net.IPNet); ok {
					if ipnet.IP.IsLoopback() || ipnet.IP.IsLinkLocalUnicast() {
						continue
					}
					ips = append(ips, ipnet.IP)
				}
			}
		}
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(now.Unix()),
		Subject:               pkix.Name{CommonName: "Distance Desktop"},
		NotBefore:             now,
		NotAfter:              now.Add(certValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{hostname, "localhost"},
		IPAddresses:           ips,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return certEntry{}, fmt.Errorf("create cert: %w", err)
	}

	tlsCert := tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  key,
	}

	fp := sha256.Sum256(der)

	certPath := filepath.Join(cm.dir, "ec-cert.pem")
	keyPath := filepath.Join(cm.dir, "ec-key.pem")

	certOut, err := os.Create(certPath)
	if err != nil {
		return certEntry{}, fmt.Errorf("write cert: %w", err)
	}
	pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: der})
	certOut.Close()

	keyBytes, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return certEntry{}, fmt.Errorf("marshal key: %w", err)
	}
	keyOut, err := os.Create(keyPath)
	if err != nil {
		return certEntry{}, fmt.Errorf("write key: %w", err)
	}
	pem.Encode(keyOut, &pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes})
	keyOut.Close()

	log.Printf("generated ECDSA P-256 cert valid until %s", tmpl.NotAfter.Format(time.RFC3339))

	return certEntry{
		Cert:        tlsCert,
		Fingerprint: fp[:],
		NotAfter:    tmpl.NotAfter,
	}, nil
}
