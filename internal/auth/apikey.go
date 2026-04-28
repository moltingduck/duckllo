package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

const (
	APIKeyPrefix = "duckllo_"
	// PrefixLookup is the inline portion stored unhashed so we can index it
	// and avoid bcrypt-comparing every key in the table on every request.
	prefixLookupBytes = 4 // 8 hex chars after duckllo_
	secretBytes       = 24
)

// MintAPIKey returns the plaintext key (shown to the user once), the
// indexable prefix, and the bcrypt hash to persist. Format is
// duckllo_<8 hex chars from the prefix>_<48 hex chars secret>.
func MintAPIKey() (plain, prefix, hash string, err error) {
	pBuf := make([]byte, prefixLookupBytes)
	sBuf := make([]byte, secretBytes)
	if _, err = rand.Read(pBuf); err != nil {
		return
	}
	if _, err = rand.Read(sBuf); err != nil {
		return
	}
	pHex := hex.EncodeToString(pBuf)
	prefix = APIKeyPrefix + pHex
	plain = prefix + "_" + hex.EncodeToString(sBuf)
	h, hErr := bcrypt.GenerateFromPassword([]byte(plain), bcryptCost)
	if hErr != nil {
		err = hErr
		return
	}
	hash = string(h)
	return
}

// ParseAPIKeyPrefix extracts the indexable portion `duckllo_<8hex>` from a
// full token. Returns ErrMalformedAPIKey if the token does not match.
func ParseAPIKeyPrefix(token string) (string, error) {
	if !strings.HasPrefix(token, APIKeyPrefix) {
		return "", ErrMalformedAPIKey
	}
	rest := token[len(APIKeyPrefix):]
	parts := strings.SplitN(rest, "_", 2)
	if len(parts) != 2 || len(parts[0]) != prefixLookupBytes*2 {
		return "", ErrMalformedAPIKey
	}
	return APIKeyPrefix + parts[0], nil
}

func CheckAPIKey(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}

var ErrMalformedAPIKey = errors.New("malformed api key")
