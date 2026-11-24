package domain

import "github.com/oklog/ulid/v2"

// UserID is a validated 26-character ULID identifying a user.
type UserID struct {
	value ulid.ULID
}

func NewUserID() UserID {
	return UserID{value: ulid.Make()}
}

func ParseUserID(raw string) (UserID, error) {
	id, err := parseULID("user_id", raw)
	if err != nil {
		return UserID{}, err
	}
	return UserID{value: id}, nil
}

func (id UserID) String() string { return id.value.String() }

func (id UserID) IsZero() bool { return id.value == ulid.ULID{} }

// TeamID is a validated 26-character ULID identifying a team.
type TeamID struct {
	value ulid.ULID
}

func NewTeamID() TeamID {
	return TeamID{value: ulid.Make()}
}

func ParseTeamID(raw string) (TeamID, error) {
	id, err := parseULID("team_id", raw)
	if err != nil {
		return TeamID{}, err
	}
	return TeamID{value: id}, nil
}

func (id TeamID) String() string { return id.value.String() }

func (id TeamID) IsZero() bool { return id.value == ulid.ULID{} }

// MembershipID is a validated 26-character ULID identifying a membership
// period. A user who leaves and rejoins a team gets a new MembershipID.
type MembershipID struct {
	value ulid.ULID
}

func NewMembershipID() MembershipID {
	return MembershipID{value: ulid.Make()}
}

func ParseMembershipID(raw string) (MembershipID, error) {
	id, err := parseULID("membership_id", raw)
	if err != nil {
		return MembershipID{}, err
	}
	return MembershipID{value: id}, nil
}

func (id MembershipID) String() string { return id.value.String() }

func (id MembershipID) IsZero() bool { return id.value == ulid.ULID{} }

// EventID is a validated 26-character ULID identifying a billing event.
type EventID struct {
	value ulid.ULID
}

func NewEventID() EventID {
	return EventID{value: ulid.Make()}
}

func ParseEventID(raw string) (EventID, error) {
	id, err := parseULID("event_id", raw)
	if err != nil {
		return EventID{}, err
	}
	return EventID{value: id}, nil
}

func (id EventID) String() string { return id.value.String() }

func (id EventID) IsZero() bool { return id.value == ulid.ULID{} }

// NewRequestID returns a canonical ULID used to correlate an API request with
// its billing events and request log. Inbound X-Request-ID values are preserved
// as-is; this generator is only for requests without one.
func NewRequestID() string {
	return ulid.Make().String()
}

// parseULID deliberately uses ParseStrict. ulid.Parse accepts shortened and
// otherwise malformed encodings which must never reach persistence.
func parseULID(field, raw string) (ulid.ULID, error) {
	id, err := ulid.ParseStrict(raw)
	if err != nil {
		return ulid.ULID{}, validationError(field, "must be a canonical 26-character ULID")
	}
	return id, nil
}
