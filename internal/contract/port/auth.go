package port

import "github.com/aiagate/flow-app-template/internal/domain"

// Authenticator resolves a bearer token to the principal it represents.
type Authenticator interface {
	Authenticate(token string) (domain.Principal, bool)
}
