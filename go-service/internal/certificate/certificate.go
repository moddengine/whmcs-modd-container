package certificate

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/moddengine/whmcs-container-controller/internal/config"
)

type Issuer struct {
	certificate     *x509.Certificate
	intermediatePEM []byte
	key             crypto.Signer
	rootPath        string
	mu              sync.Mutex
}

func Load(cfg config.Certificates) (*Issuer, error) {
	certificatePEM, err := os.ReadFile(cfg.IntermediateCertificate)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(certificatePEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("intermediate certificate is not PEM encoded")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	if !certificate.IsCA || certificate.KeyUsage&x509.KeyUsageCertSign == 0 {
		return nil, errors.New("intermediate certificate is not a CA")
	}
	keyPEM, err := os.ReadFile(cfg.IntermediateKey)
	if err != nil {
		return nil, err
	}
	key, err := parseSigner(keyPEM)
	if err != nil {
		return nil, err
	}
	public, err := x509.MarshalPKIXPublicKey(key.Public())
	if err != nil {
		return nil, err
	}
	if certificatePublic, marshalErr := x509.MarshalPKIXPublicKey(certificate.PublicKey); marshalErr != nil || !bytes.Equal(public, certificatePublic) {
		return nil, errors.New("intermediate certificate and key do not match")
	}
	rootPEM, err := os.ReadFile(cfg.RootCertificate)
	if err != nil {
		return nil, err
	}
	rootBlock, _ := pem.Decode(rootPEM)
	if rootBlock == nil || rootBlock.Type != "CERTIFICATE" {
		return nil, errors.New("root certificate is not PEM encoded")
	}
	return &Issuer{certificate: certificate, intermediatePEM: pem.EncodeToMemory(block), key: key, rootPath: cfg.RootCertificate}, nil
}

func (i *Issuer) Issue(dir, id string, newKey bool, uid, gid int) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := os.MkdirAll(dir, 0750); err != nil {
		return err
	}
	var rootPEM []byte
	var err error
	if newKey {
		rootPEM, err = os.ReadFile(i.rootPath)
		if err != nil {
			return err
		}
		if block, _ := pem.Decode(rootPEM); block == nil || block.Type != "CERTIFICATE" {
			return errors.New("root certificate is not PEM encoded")
		}
	}
	keyPath := filepath.Join(dir, "ident.key")
	var key *ecdsa.PrivateKey
	if newKey {
		key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	} else {
		key, err = readIdentityKey(keyPath)
	}
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if now.Before(i.certificate.NotBefore) || now.Add(32*time.Hour).After(i.certificate.NotAfter) {
		return errors.New("intermediate certificate is not valid for the identity certificate lifetime")
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: id},
		DNSNames:     []string{id},
		NotBefore:    now,
		NotAfter:     now.Add(32 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, i.certificate, &key.PublicKey, i.key)
	if err != nil {
		return err
	}
	if newKey {
		keyDER, marshalErr := x509.MarshalPKCS8PrivateKey(key)
		if marshalErr != nil {
			return marshalErr
		}
		if err := atomicWrite(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600, uid, gid); err != nil {
			return err
		}
	}
	chain := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), i.intermediatePEM...)
	if err := atomicWrite(filepath.Join(dir, "ident.crt"), chain, 0644, uid, gid); err != nil {
		return err
	}
	if newKey {
		return atomicWrite(filepath.Join(dir, "root.crt"), rootPEM, 0644, uid, gid)
	}
	return nil
}

func readIdentityKey(path string) (*ecdsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("identity key is not PEM encoded")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("identity key is not ECDSA")
	}
	return key, nil
}

func parseSigner(data []byte) (crypto.Signer, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("intermediate key is not PEM encoded")
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if signer, ok := key.(crypto.Signer); ok {
			return signer, nil
		}
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	if key, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	return nil, errors.New("intermediate key is not a supported private key")
}

func atomicWrite(path string, content []byte, mode os.FileMode, uid, gid int) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err = tmp.Chmod(mode); err == nil {
		err = tmp.Chown(uid, gid)
	}
	if err == nil {
		_, err = tmp.Write(content)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("replace %s: %w", filepath.Base(path), err)
	}
	return nil
}
