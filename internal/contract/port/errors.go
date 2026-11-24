package port

import (
	"errors"
	"fmt"
)

// Repository sentinel errors. Implementations wrap these so callers can use
// errors.Is without depending on driver-specific errors.
var (
	ErrNotFound        = errors.New("repository: not found")
	ErrAlreadyExists   = errors.New("repository: already exists")
	ErrVersionConflict = errors.New("repository: version conflict")
)

// RepositoryError adds the failed operation and, when available, the
// underlying cause to a sentinel repository error.
type RepositoryError struct {
	Op      string
	Message string
	Err     error
}

func (e *RepositoryError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("%s: %v", e.Op, e.Err)
	}
	return fmt.Sprintf("%s: %s: %v", e.Op, e.Message, e.Err)
}

func (e *RepositoryError) Unwrap() error { return e.Err }
