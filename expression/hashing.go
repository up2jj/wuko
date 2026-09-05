package expression

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"hash"
	"strings"
)

// MD5 returns an MD5 checksum. It is provided for compatibility, not security.
func MD5(value string, options map[string]any) (string, error) {
	return digest(value, options, md5.New)
}

// SHA1 returns a SHA-1 checksum. It is provided for compatibility, not security.
func SHA1(value string, options map[string]any) (string, error) {
	return digest(value, options, sha1.New)
}

// SHA256 returns a SHA-256 digest.
func SHA256(value string, options map[string]any) (string, error) {
	return digest(value, options, sha256.New)
}

// SHA512 returns a SHA-512 digest.
func SHA512(value string, options map[string]any) (string, error) {
	return digest(value, options, sha512.New)
}

// HMACSHA256 returns an HMAC-SHA256 authentication code.
func HMACSHA256(value, key string, options map[string]any) (string, error) {
	return hmacDigest(value, key, options, sha256.New)
}

// HMACSHA512 returns an HMAC-SHA512 authentication code.
func HMACSHA512(value, key string, options map[string]any) (string, error) {
	return hmacDigest(value, key, options, sha512.New)
}

func digest(value string, options map[string]any, newHash func() hash.Hash) (string, error) {
	hasher := newHash()
	_, _ = hasher.Write([]byte(value))
	return formatDigest(hasher.Sum(nil), options)
}

func hmacDigest(value, key string, options map[string]any, newHash func() hash.Hash) (string, error) {
	hasher := hmac.New(newHash, []byte(key))
	_, _ = hasher.Write([]byte(value))
	return formatDigest(hasher.Sum(nil), options)
}

func formatDigest(value []byte, options map[string]any) (string, error) {
	encoding := "hex"
	uppercase := false
	for name, raw := range options {
		switch name {
		case "encoding":
			var ok bool
			encoding, ok = raw.(string)
			if !ok {
				return "", fmt.Errorf("digest option %q must be a string, got %T", name, raw)
			}
		case "uppercase":
			var ok bool
			uppercase, ok = raw.(bool)
			if !ok {
				return "", fmt.Errorf("digest option %q must be a boolean, got %T", name, raw)
			}
		default:
			return "", fmt.Errorf("unknown digest option %q", name)
		}
	}
	switch encoding {
	case "hex":
		result := hex.EncodeToString(value)
		if uppercase {
			result = strings.ToUpper(result)
		}
		return result, nil
	case "base64":
		if uppercase {
			return "", fmt.Errorf("digest option %q is valid only with hex encoding", "uppercase")
		}
		return base64.StdEncoding.EncodeToString(value), nil
	default:
		return "", fmt.Errorf("digest encoding must be %q or %q", "hex", "base64")
	}
}
