package aaphone

import (
	"crypto/tls"
	"os"
	"path/filepath"

	"dexd/aatls"
)

// LoadCert returns the certificate dexd presents to the car.
//
// The supplied pair (md_cert.pem and md_key.pem in suppliedDir) is used when
// both files exist and parse. Otherwise a self-generated pair kept in dataDir
// (self_cert.pem, self_key.pem) is used, created on first use. If dataDir
// cannot be written the generated pair is still returned, just not kept.
func LoadCert(suppliedDir, dataDir string) (cert tls.Certificate, selfSigned bool, err error) {
	if c, err := tls.LoadX509KeyPair(
		filepath.Join(suppliedDir, "md_cert.pem"),
		filepath.Join(suppliedDir, "md_key.pem"),
	); err == nil {
		return c, false, nil
	}

	certPath, keyPath := filepath.Join(dataDir, "self_cert.pem"), filepath.Join(dataDir, "self_key.pem")
	if c, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		return c, true, nil
	}

	certPEM, keyPEM, err := aatls.GeneratePEM()
	if err != nil {
		return tls.Certificate{}, true, err
	}
	if os.MkdirAll(dataDir, 0o700) == nil {
		if os.WriteFile(keyPath, keyPEM, 0o600) == nil {
			os.WriteFile(certPath, certPEM, 0o644)
		}
	}
	c, err := tls.X509KeyPair(certPEM, keyPEM)
	return c, true, err
}
