package domain

import (
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	displayNameMaxRunes = 100
	nameMaxRunes        = 100
	emailMaxLength      = 254
)

// DisplayName is a non-empty, trimmed name of at most 100 runes.
type DisplayName struct {
	value string
}

func NewDisplayName(raw string) (DisplayName, error) {
	if raw == "" {
		return DisplayName{}, validationError("display_name", "must not be empty")
	}
	if utf8.RuneCountInString(raw) > displayNameMaxRunes {
		return DisplayName{}, validationError("display_name", "must not exceed 100 characters")
	}
	if raw != strings.TrimSpace(raw) {
		return DisplayName{}, validationError("display_name", "must not have leading or trailing whitespace")
	}
	return DisplayName{value: raw}, nil
}

func (n DisplayName) String() string { return n.value }

func (n DisplayName) IsZero() bool { return n.value == "" }

// emailPattern mirrors the RFC 5322-ish rules the template has always used:
// a dotted local part, a dotted domain and an alphabetic TLD of at least two
// characters.
var emailPattern = regexp.MustCompile(`^[a-zA-Z0-9_%+\-]+(?:\.[a-zA-Z0-9_%+\-]+)*@[a-zA-Z0-9\-]+(?:\.[a-zA-Z0-9\-]+)*\.[a-zA-Z]{2,}$`)

// Email is a trimmed, validated email address.
type Email struct {
	value string
}

func NewEmail(raw string) (Email, error) {
	normalized := strings.TrimSpace(raw)
	if normalized == "" {
		return Email{}, validationError("email", "must not be empty")
	}
	if len(normalized) > emailMaxLength {
		return Email{}, validationError("email", "must not exceed 254 characters")
	}
	if !emailPattern.MatchString(normalized) {
		return Email{}, validationError("email", "must be a valid email address")
	}
	domainPart := normalized[strings.LastIndexByte(normalized, '@')+1:]
	for _, label := range strings.Split(domainPart, ".") {
		if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return Email{}, validationError("email", "must be a valid email address")
		}
	}
	return Email{value: normalized}, nil
}

func (e Email) String() string { return e.value }

func (e Email) IsZero() bool { return e.value == "" }

// Version is the optimistic-locking generation of an aggregate.
type Version struct {
	value int64
}

func NewVersion(value int64) (Version, error) {
	if value < 0 {
		return Version{}, validationError("version", "must be non-negative")
	}
	return Version{value: value}, nil
}

func (v Version) Next() Version { return Version{value: v.value + 1} }

func (v Version) Int64() int64 { return v.value }

func (v Version) IsZero() bool { return v.value == 0 }

// User is the user aggregate root.
type User struct {
	id          UserID
	displayName DisplayName
	email       Email
	version     Version
	createdAt   time.Time
	updatedAt   time.Time
}

// RegisterUser creates a new user at version zero.
func RegisterUser(displayName DisplayName, email Email) *User {
	now := time.Now().UTC()
	return &User{
		id:          NewUserID(),
		displayName: displayName,
		email:       email,
		version:     Version{},
		createdAt:   now,
		updatedAt:   now,
	}
}

// RestoreUser rebuilds an aggregate from persisted state.
func RestoreUser(
	id UserID,
	displayName DisplayName,
	email Email,
	version Version,
	createdAt time.Time,
	updatedAt time.Time,
) *User {
	return &User{
		id:          id,
		displayName: displayName,
		email:       email,
		version:     version,
		createdAt:   createdAt,
		updatedAt:   updatedAt,
	}
}

func (u *User) ID() UserID               { return u.id }
func (u *User) DisplayName() DisplayName { return u.displayName }
func (u *User) Email() Email             { return u.email }
func (u *User) Version() Version         { return u.version }
func (u *User) CreatedAt() time.Time     { return u.createdAt }
func (u *User) UpdatedAt() time.Time     { return u.updatedAt }
func (u *User) ChangeEmail(email Email)  { u.email = email; u.touch() }
func (u *User) touch()                   { u.updatedAt = time.Now().UTC() }

// Equal compares aggregate identity, not mutable state.
func (u *User) Equal(other *User) bool {
	return u != nil && other != nil && u.id == other.id
}
