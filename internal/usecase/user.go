package usecase

import (
	"context"

	"github.com/aiagate/flow-app-template/internal/contract/port"
	"github.com/aiagate/flow-app-template/internal/domain"
)

// UserUseCase applies user directory operations.
type UserUseCase struct {
	tx    port.WriteTxManager
	users port.UserRepository
}

func NewUserUseCase(tx port.WriteTxManager, users port.UserRepository) *UserUseCase {
	return &UserUseCase{tx: tx, users: users}
}

type CreateUserInput struct {
	DisplayName string
	Email       string
}

// CreateUser validates the value objects and persists a new user aggregate.
func (u *UserUseCase) CreateUser(ctx context.Context, input CreateUserInput) (*domain.User, error) {
	displayName, err := domain.NewDisplayName(input.DisplayName)
	if err != nil {
		return nil, ClassifyError(err)
	}
	email, err := domain.NewEmail(input.Email)
	if err != nil {
		return nil, ClassifyError(err)
	}

	user := domain.RegisterUser(displayName, email)
	err = u.tx.WithWriteTx(ctx, func(ctx context.Context, repos port.WriteRepos) error {
		return repos.Users().Create(ctx, user)
	})
	if err != nil {
		return nil, ClassifyError(err)
	}
	return user, nil
}

// GetUser loads one user by its canonical ULID.
func (u *UserUseCase) GetUser(ctx context.Context, id string) (*domain.User, error) {
	userID, err := domain.ParseUserID(id)
	if err != nil {
		return nil, ClassifyError(err)
	}
	user, err := u.users.GetByID(ctx, userID)
	if err != nil {
		return nil, ClassifyError(err)
	}
	return user, nil
}

// ListUsers returns every user ordered by the repository's stable ordering.
func (u *UserUseCase) ListUsers(ctx context.Context) ([]*domain.User, error) {
	users, err := u.users.List(ctx)
	if err != nil {
		return nil, ClassifyError(err)
	}
	return users, nil
}

type ChangeUserEmailInput struct {
	UserID          string
	Email           string
	ExpectedVersion *int64
}

// ChangeUserEmail updates the email with optimistic locking. When
// ExpectedVersion is omitted the version loaded inside the write transaction is
// used, so a concurrent writer still produces a version conflict.
func (u *UserUseCase) ChangeUserEmail(ctx context.Context, input ChangeUserEmailInput) (*domain.User, error) {
	userID, err := domain.ParseUserID(input.UserID)
	if err != nil {
		return nil, ClassifyError(err)
	}
	email, err := domain.NewEmail(input.Email)
	if err != nil {
		return nil, ClassifyError(err)
	}
	if err := validateExpectedVersion(input.ExpectedVersion); err != nil {
		return nil, err
	}

	var updated *domain.User
	err = u.tx.WithWriteTx(ctx, func(ctx context.Context, repos port.WriteRepos) error {
		user, err := repos.Users().GetByID(ctx, userID)
		if err != nil {
			return err
		}
		if input.ExpectedVersion != nil && user.Version().Int64() != *input.ExpectedVersion {
			return port.ErrVersionConflict
		}
		user.ChangeEmail(email)
		if err := repos.Users().Update(ctx, user); err != nil {
			return err
		}
		updated, err = repos.Users().GetByID(ctx, userID)
		return err
	})
	if err != nil {
		return nil, ClassifyError(err)
	}
	return updated, nil
}

func validateExpectedVersion(expected *int64) error {
	if expected == nil {
		return nil
	}
	if *expected < 0 {
		return validationError("expected_version", "must be non-negative")
	}
	return nil
}
