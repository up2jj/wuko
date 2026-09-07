package forward_proxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type certificateAuthority struct {
	certificate *x509.Certificate
	key         *ecdsa.PrivateKey
	pem         []byte
	sha256      string
	mu          sync.Mutex
	leaves      map[string]*tls.Certificate
}

func newCertificateAuthority(now time.Time) (*certificateAuthority, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating CA key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "Wuko Forward Proxy"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("creating CA certificate: %w", err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parsing generated CA certificate: %w", err)
	}
	digest := sha256.Sum256(der)
	return &certificateAuthority{certificate: certificate, key: key,
		pem:    pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		sha256: hex.EncodeToString(digest[:]), leaves: make(map[string]*tls.Certificate)}, nil
}

func (authority *certificateAuthority) certificateFor(host string) (*tls.Certificate, error) {
	host = strings.TrimSuffix(strings.TrimSpace(host), ".")
	if host == "" {
		return nil, fmt.Errorf("TLS server name is empty")
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if certificate := authority.leaves[host]; certificate != nil {
		return certificate, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating certificate key for %s: %w", host, err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: host},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: authority.certificate.NotAfter,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		template.IPAddresses = []net.IP{ip}
	} else {
		template.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, authority.certificate, &key.PublicKey, authority.key)
	if err != nil {
		return nil, fmt.Errorf("creating certificate for %s: %w", host, err)
	}
	certificate := &tls.Certificate{Certificate: [][]byte{der, authority.certificate.Raw}, PrivateKey: key}
	authority.leaves[host] = certificate
	return certificate, nil
}

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("generating certificate serial: %w", err)
	}
	return serial, nil
}

func writeCACertificate(runDir string, certificate []byte) (string, error) {
	if runDir == "" {
		runDir = os.TempDir()
	}
	file, err := os.CreateTemp(runDir, ".wuko-forward-proxy-ca-*.pem")
	if err != nil {
		return "", fmt.Errorf("creating CA certificate file: %w", err)
	}
	path := filepath.Clean(file.Name())
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		os.Remove(path)
		return "", fmt.Errorf("setting CA certificate permissions: %w", err)
	}
	if _, err := file.Write(certificate); err != nil {
		file.Close()
		os.Remove(path)
		return "", fmt.Errorf("writing CA certificate: %w", err)
	}
	if err := file.Close(); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("closing CA certificate: %w", err)
	}
	return path, nil
}
