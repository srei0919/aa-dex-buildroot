package aaphone

import (
	"bytes"
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCertGeneratesOnce(t *testing.T) {
	supplied, data := t.TempDir(), filepath.Join(t.TempDir(), "dexd")
	c1, self, err := LoadCert(supplied, data)
	if err != nil || !self {
		t.Fatalf("self %v err %v", self, err)
	}
	c2, self2, err := LoadCert(supplied, data)
	if err != nil || !self2 || !bytes.Equal(c1.Certificate[0], c2.Certificate[0]) {
		t.Fatalf("second load differs (self %v err %v)", self2, err)
	}
	leaf, err := x509.ParseCertificate(c1.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if leaf.NotBefore.Year() != 2020 || leaf.NotAfter.Year() != 2045 || leaf.Subject.CommonName != "CarDeX" {
		t.Errorf("validity %v..%v cn %q", leaf.NotBefore, leaf.NotAfter, leaf.Subject.CommonName)
	}
	if st, err := os.Stat(filepath.Join(data, "self_key.pem")); err != nil || st.Size() == 0 {
		t.Errorf("key file: %v", err)
	}
}

func TestLoadCertPrefersSupplied(t *testing.T) {
	// A pair generated into one directory stands in for user-supplied files in another.
	gen := t.TempDir()
	want, _, err := LoadCert(t.TempDir(), gen)
	if err != nil {
		t.Fatal(err)
	}
	supplied := t.TempDir()
	copyFile(t, filepath.Join(gen, "self_cert.pem"), filepath.Join(supplied, "md_cert.pem"))
	copyFile(t, filepath.Join(gen, "self_key.pem"), filepath.Join(supplied, "md_key.pem"))
	got, self, err := LoadCert(supplied, t.TempDir())
	if err != nil || self || !bytes.Equal(got.Certificate[0], want.Certificate[0]) {
		t.Fatalf("self %v err %v", self, err)
	}
}

func TestLoadCertBadSuppliedFallsBack(t *testing.T) {
	supplied := t.TempDir()
	os.WriteFile(filepath.Join(supplied, "md_cert.pem"), []byte("garbage"), 0o600)
	os.WriteFile(filepath.Join(supplied, "md_key.pem"), []byte("garbage"), 0o600)
	_, self, err := LoadCert(supplied, t.TempDir())
	if err != nil || !self {
		t.Fatalf("self %v err %v", self, err)
	}
}

func TestLoadCertUnwritableDataDir(t *testing.T) {
	file := filepath.Join(t.TempDir(), "plainfile")
	os.WriteFile(file, []byte("x"), 0o600)
	c, self, err := LoadCert(t.TempDir(), filepath.Join(file, "sub"))
	if err != nil || !self || len(c.Certificate) == 0 {
		t.Fatalf("self %v err %v", self, err)
	}
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	b, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, b, 0o600); err != nil {
		t.Fatal(err)
	}
}
