package certificate

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/moddengine/whmcs-container-controller/internal/config"
)

func TestIssueAndRenewIdentity(t *testing.T) {
	root := t.TempDir()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "local intermediate"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(1, 0, 0),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(caKey)
	if err != nil {
		t.Fatal(err)
	}
	caPath, keyPath, rootPath := filepath.Join(root, "ca.crt"), filepath.Join(root, "ca.key"), filepath.Join(root, "root.crt")
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	for path, data := range map[string][]byte{
		caPath: caPEM, keyPath: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), rootPath: caPEM,
	} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	issuer, err := Load(config.Certificates{RootCertificate: rootPath, IntermediateCertificate: caPath, IntermediateKey: keyPath})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "secrets")
	if err := issuer.Issue(dir, "whmcs-123", true, os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	identityKey, _ := os.ReadFile(filepath.Join(dir, "ident.key"))
	firstCertificate := readCertificate(t, filepath.Join(dir, "ident.crt"))
	if countCertificates(t, filepath.Join(dir, "ident.crt")) != 2 {
		t.Fatal("identity certificate does not contain the leaf and intermediate chain")
	}
	if firstCertificate.Subject.CommonName != "whmcs-123" || firstCertificate.NotAfter.Sub(firstCertificate.NotBefore) != 32*time.Hour {
		t.Fatalf("unexpected identity certificate: %#v", firstCertificate)
	}
	if err := firstCertificate.CheckSignatureFrom(issuer.certificate); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "root.crt"), []byte("leave on renewal"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := issuer.Issue(dir, "whmcs-123", false, os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	renewedKey, _ := os.ReadFile(filepath.Join(dir, "ident.key"))
	renewedRoot, _ := os.ReadFile(filepath.Join(dir, "root.crt"))
	if !bytes.Equal(identityKey, renewedKey) || string(renewedRoot) != "leave on renewal" {
		t.Fatal("renewal changed the identity key or root certificate")
	}
	replacementRoot := append(append([]byte(nil), caPEM...), '\n')
	if err := os.WriteFile(rootPath, replacementRoot, 0600); err != nil {
		t.Fatal(err)
	}
	if err := issuer.Issue(dir, "whmcs-123", true, os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	rotatedKey, _ := os.ReadFile(filepath.Join(dir, "ident.key"))
	rotatedRoot, _ := os.ReadFile(filepath.Join(dir, "root.crt"))
	if bytes.Equal(identityKey, rotatedKey) || !bytes.Equal(rotatedRoot, replacementRoot) {
		t.Fatal("new identity key did not replace the key and configured root certificate")
	}
}

func readCertificate(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(data)
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return certificate
}

func countCertificates(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for block, rest := pem.Decode(data); block != nil; block, rest = pem.Decode(rest) {
		if block.Type == "CERTIFICATE" {
			count++
		}
	}
	return count
}
