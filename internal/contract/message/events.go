// Package message holds shared DTOs and topic names that cross architectural
// boundaries.
package message

import (
	"time"

	"github.com/aiagate/flow-app-template/internal/domain"
)

const UserCreatedTopic = "user.created"

type UserCreatedEvent struct {
	UserID      domain.UserID
	DisplayName string
	Email       string
	OccurredAt  time.Time
}
