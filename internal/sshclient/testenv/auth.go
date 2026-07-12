// Package testenv provides reusable SSH servers and fault injectors for tests.
package testenv

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"time"

	"golang.org/x/crypto/ssh"
)

// Auth contains a generated client identity and matching SSH configurations.
type Auth struct {
	User       string
	PrivateKey []byte
	PublicKey  ssh.PublicKey
	Signer     ssh.Signer
}

// NewAuth generates an Ed25519 client identity for user.
func NewAuth(user string) (*Auth, error) {
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate client key: %w", err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		return nil, fmt.Errorf("create client signer: %w", err)
	}
	publicKey, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("create client public key: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return nil, fmt.Errorf("marshal client key: %w", err)
	}
	return &Auth{
		User:       user,
		PrivateKey: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}),
		PublicKey:  publicKey,
		Signer:     signer,
	}, nil
}

// ClientConfig returns a client configuration that trusts the ephemeral host.
func (a *Auth) ClientConfig() *ssh.ClientConfig {
	return &ssh.ClientConfig{
		User:            a.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(a.Signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // Test-only ephemeral host.
		Timeout:         5 * time.Second,
	}
}

// ServerConfig returns a server configuration accepting only this identity.
func (a *Auth) ServerConfig(hostKey ssh.Signer) *ssh.ServerConfig {
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if meta.User() == a.User && string(key.Marshal()) == string(a.PublicKey.Marshal()) {
				return &ssh.Permissions{}, nil
			}
			return nil, fmt.Errorf("public key rejected")
		},
	}
	cfg.AddHostKey(hostKey)
	return cfg
}
