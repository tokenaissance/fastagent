// Package cryptoutil defines the neutral at-rest credential encryption
// contract shared by every storage adapter that persists secrets (MCP OAuth
// tokens, sandbox lease tokens). Keeping the interface here — instead of
// duplicating it in each consumer package — lets unrelated adapters reuse
// the same cryptor implementations without depending on each other.
package cryptoutil

import "context"

// Cipher encrypts/decrypts stored credentials. Implementations must return
// the ciphertext with any nonce/auth data required for decryption embedded
// (e.g. AESGCMCryptor prefixes the nonce), so Decrypt needs no extra state.
type Cipher interface {
	Encrypt(ctx context.Context, plaintext []byte) ([]byte, error)
	Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error)
}
