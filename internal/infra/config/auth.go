package config

import (
	"crypto/sha256"
	"crypto/subtle"
	"sort"

	"github.com/aiagate/flow-app-template/internal/contract/port"
	"github.com/aiagate/flow-app-template/internal/domain"
)

// accountKey is one pre-hashed account token. Entries are kept in account ID
// order so construction and lookups are deterministic.
type accountKey struct {
	accountID string
	digest    [sha256.Size]byte
}

// StaticAuthenticator resolves bearer tokens against a fixed set of configured
// keys. Tokens are hashed once at construction and compared in constant time.
type StaticAuthenticator struct {
	adminDigest [sha256.Size]byte
	hasAdmin    bool
	accountKeys []accountKey
}

var _ port.Authenticator = (*StaticAuthenticator)(nil)

// NewStaticAuthenticator pre-hashes the admin key and every account token.
// Duplicate and admin-colliding tokens are rejected by Load, but authentication
// remains well-defined if this constructor receives them: the admin match wins.
func NewStaticAuthenticator(adminKey string, accountKeys map[string]string) *StaticAuthenticator {
	authenticator := &StaticAuthenticator{}
	if adminKey != "" {
		authenticator.adminDigest = sha256.Sum256([]byte(adminKey))
		authenticator.hasAdmin = true
	}

	accountIDs := make([]string, 0, len(accountKeys))
	for accountID := range accountKeys {
		accountIDs = append(accountIDs, accountID)
	}
	sort.Strings(accountIDs)

	authenticator.accountKeys = make([]accountKey, 0, len(accountIDs))
	for _, accountID := range accountIDs {
		authenticator.accountKeys = append(authenticator.accountKeys, accountKey{
			accountID: accountID,
			digest:    sha256.Sum256([]byte(accountKeys[accountID])),
		})
	}
	return authenticator
}

// Authenticate resolves token to either the admin principal or an account
// principal. Every configured digest is compared without early exit so lookup
// time does not reveal which entry, if any, matched.
func (a *StaticAuthenticator) Authenticate(token string) (domain.Principal, bool) {
	if token == "" {
		return domain.Principal{}, false
	}

	digest := sha256.Sum256([]byte(token))
	adminMatch := 0
	if a.hasAdmin {
		adminMatch = subtle.ConstantTimeCompare(digest[:], a.adminDigest[:])
	}

	matchedAccount := -1
	for index := range a.accountKeys {
		if subtle.ConstantTimeCompare(digest[:], a.accountKeys[index].digest[:]) == 1 && matchedAccount < 0 {
			matchedAccount = index
		}
	}

	if adminMatch == 1 {
		return domain.Principal{Subject: "admin", IsAdmin: true}, true
	}
	if matchedAccount >= 0 {
		accountID := a.accountKeys[matchedAccount].accountID
		return domain.Principal{
			Subject:   "account:" + accountID,
			AccountID: accountID,
			IsAdmin:   false,
		}, true
	}
	return domain.Principal{}, false
}
