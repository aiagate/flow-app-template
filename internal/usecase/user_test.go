package usecase

import (
	"context"
	"errors"
	"testing"

	"github.com/aiagate/flow-app-template/internal/contract/port"
)

func newUserUseCaseForTest() (*UserUseCase, *fakeRepos) {
	repos := newFakeRepos()
	return NewUserUseCase(repos, repos.Users()), repos
}

func TestCreateAndReadUsers(t *testing.T) {
	ctx := context.Background()
	users, _ := newUserUseCaseForTest()

	created, err := users.CreateUser(ctx, CreateUserInput{DisplayName: "Alice", Email: "alice@example.com"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if created.ID().IsZero() {
		t.Fatal("created user has a zero ID")
	}
	if created.Version().Int64() != 0 {
		t.Fatalf("version = %d, want 0", created.Version().Int64())
	}

	loaded, err := users.GetUser(ctx, created.ID().String())
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if loaded.Email().String() != "alice@example.com" {
		t.Fatalf("email = %q", loaded.Email().String())
	}

	second, err := users.CreateUser(ctx, CreateUserInput{DisplayName: "Bob", Email: "bob@example.com"})
	if err != nil {
		t.Fatalf("CreateUser(second): %v", err)
	}
	list, err := users.ListUsers(ctx)
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(list) != 2 || list[0].ID() != created.ID() || list[1].ID() != second.ID() {
		t.Fatalf("ListUsers = %+v", list)
	}
}

func TestCreateUserValidationAndConflicts(t *testing.T) {
	ctx := context.Background()
	users, _ := newUserUseCaseForTest()

	cases := []struct {
		name  string
		input CreateUserInput
		code  ErrorCode
	}{
		{"empty display name", CreateUserInput{DisplayName: "", Email: "a@example.com"}, CodeValidation},
		{"padded display name", CreateUserInput{DisplayName: " Alice", Email: "a@example.com"}, CodeValidation},
		{"invalid email", CreateUserInput{DisplayName: "Alice", Email: "not-an-email"}, CodeValidation},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := users.CreateUser(ctx, testCase.input)
			assertCode(t, err, testCase.code)
		})
	}

	if _, err := users.CreateUser(ctx, CreateUserInput{DisplayName: "Alice", Email: "alice@example.com"}); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	_, err := users.CreateUser(ctx, CreateUserInput{DisplayName: "Other", Email: "alice@example.com"})
	assertCode(t, err, CodeConflict)
}

func TestChangeUserEmailOptimisticLocking(t *testing.T) {
	ctx := context.Background()
	users, _ := newUserUseCaseForTest()
	created, err := users.CreateUser(ctx, CreateUserInput{DisplayName: "Alice", Email: "alice@example.com"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	updated, err := users.ChangeUserEmail(ctx, ChangeUserEmailInput{
		UserID: created.ID().String(),
		Email:  "new@example.com",
	})
	if err != nil {
		t.Fatalf("ChangeUserEmail: %v", err)
	}
	if updated.Email().String() != "new@example.com" {
		t.Fatalf("email = %q", updated.Email().String())
	}
	if updated.Version().Int64() != 1 {
		t.Fatalf("version = %d, want 1", updated.Version().Int64())
	}

	expected := int64(1)
	updated, err = users.ChangeUserEmail(ctx, ChangeUserEmailInput{
		UserID:          created.ID().String(),
		Email:           "newer@example.com",
		ExpectedVersion: &expected,
	})
	if err != nil {
		t.Fatalf("ChangeUserEmail(expected=1): %v", err)
	}
	if updated.Version().Int64() != 2 {
		t.Fatalf("version = %d, want 2", updated.Version().Int64())
	}

	stale := int64(0)
	_, err = users.ChangeUserEmail(ctx, ChangeUserEmailInput{
		UserID:          created.ID().String(),
		Email:           "stale@example.com",
		ExpectedVersion: &stale,
	})
	assertCode(t, err, CodeConcurrencyConflict)

	loaded, err := users.GetUser(ctx, created.ID().String())
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if loaded.Email().String() != "newer@example.com" {
		t.Fatalf("stale update changed email to %q", loaded.Email().String())
	}

	negative := int64(-1)
	_, err = users.ChangeUserEmail(ctx, ChangeUserEmailInput{
		UserID:          created.ID().String(),
		Email:           "x@example.com",
		ExpectedVersion: &negative,
	})
	assertCode(t, err, CodeValidation)

	_, err = users.ChangeUserEmail(ctx, ChangeUserEmailInput{
		UserID: "not-a-ulid",
		Email:  "x@example.com",
	})
	assertCode(t, err, CodeValidation)

	missing := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	_, err = users.ChangeUserEmail(ctx, ChangeUserEmailInput{UserID: missing, Email: "x@example.com"})
	assertCode(t, err, CodeNotFound)

	_, err = users.GetUser(ctx, missing)
	assertCode(t, err, CodeNotFound)
}

func TestUserRepositoryFailureIsClassified(t *testing.T) {
	ctx := context.Background()
	repos := newFakeRepos()
	users := NewUserUseCase(failingTxManager{err: port.ErrNotFound}, repos.Users())
	_, err := users.CreateUser(ctx, CreateUserInput{DisplayName: "Alice", Email: "alice@example.com"})
	assertCode(t, err, CodeNotFound)
}

type failingTxManager struct{ err error }

func (f failingTxManager) WithWriteTx(context.Context, func(context.Context, port.WriteRepos) error) error {
	return f.err
}

func assertCode(t *testing.T, err error, want ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want %s", want)
	}
	classified := ClassifyError(err)
	if classified.Code != want {
		t.Fatalf("code = %s (%v), want %s", classified.Code, err, want)
	}
	if classified.Code == CodeUnexpected && !errors.Is(classified, port.ErrNotFound) {
		t.Logf("unexpected classification kept cause: %v", err)
	}
}
