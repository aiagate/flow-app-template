package usecase

import (
	"context"
	"testing"

	"github.com/aiagate/flow-app-template/internal/domain"
)

func newMembershipUseCaseForTest() (*MembershipUseCase, *TeamUseCase, *UserUseCase, *fakeRepos) {
	repos := newFakeRepos()
	return NewMembershipUseCase(repos, repos.Teams(), repos.Users(), repos.Memberships()),
		NewTeamUseCase(repos, repos.Teams()),
		NewUserUseCase(repos, repos.Users()),
		repos
}

func TestJoinLeaveAndRejoinMembership(t *testing.T) {
	ctx := context.Background()
	memberships, teams, users, _ := newMembershipUseCaseForTest()
	team := mustCreateTeam(t, ctx, teams, "Platform")
	user := mustCreateUser(t, ctx, users, "Alice", "alice@example.com")

	joined, err := memberships.JoinTeam(ctx, JoinTeamInput{
		TeamID: team.ID().String(), UserID: user.ID().String(),
	})
	if err != nil {
		t.Fatalf("JoinTeam: %v", err)
	}
	if joined.Status() != domain.StatusActive || joined.Role() != domain.RoleMember {
		t.Fatalf("joined membership = %+v", joined)
	}

	_, err = memberships.JoinTeam(ctx, JoinTeamInput{
		TeamID: team.ID().String(), UserID: user.ID().String(),
	})
	assertCode(t, err, CodeConflict)

	_, err = memberships.RequestJoinTeam(ctx, JoinTeamInput{
		TeamID: team.ID().String(), UserID: user.ID().String(),
	})
	assertCode(t, err, CodeConflict)

	left, err := memberships.LeaveTeam(ctx, joined.ID().String())
	if err != nil {
		t.Fatalf("LeaveTeam: %v", err)
	}
	if left.Status() != domain.StatusLeaved || left.Version().Int64() != 1 {
		t.Fatalf("left membership = %+v", left)
	}

	_, err = memberships.LeaveTeam(ctx, joined.ID().String())
	assertCode(t, err, CodeConflict)

	rejoined, err := memberships.JoinTeam(ctx, JoinTeamInput{
		TeamID: team.ID().String(), UserID: user.ID().String(),
	})
	if err != nil {
		t.Fatalf("rejoin: %v", err)
	}
	if rejoined.ID() == joined.ID() {
		t.Fatal("rejoin must create a fresh membership period")
	}
	if rejoined.Status() != domain.StatusActive {
		t.Fatalf("rejoined status = %q", rejoined.Status())
	}

	history, err := memberships.ListTeamMemberships(ctx, team.ID().String())
	if err != nil {
		t.Fatalf("ListTeamMemberships: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("history length = %d, want 2", len(history))
	}
	if history[0].ID() != joined.ID() || history[0].Status() != domain.StatusLeaved {
		t.Fatalf("first history row = %+v", history[0])
	}
	if history[1].ID() != rejoined.ID() || history[1].Status() != domain.StatusActive {
		t.Fatalf("second history row = %+v", history[1])
	}
}

func TestRequestApproveAndChangeRole(t *testing.T) {
	ctx := context.Background()
	memberships, teams, users, _ := newMembershipUseCaseForTest()
	team := mustCreateTeam(t, ctx, teams, "Platform")
	user := mustCreateUser(t, ctx, users, "Bob", "bob@example.com")

	pending, err := memberships.RequestJoinTeam(ctx, JoinTeamInput{
		TeamID: team.ID().String(), UserID: user.ID().String(),
	})
	if err != nil {
		t.Fatalf("RequestJoinTeam: %v", err)
	}
	if pending.Status() != domain.StatusPending {
		t.Fatalf("status = %q, want PENDING", pending.Status())
	}

	approved, err := memberships.ApproveJoinRequest(ctx, pending.ID().String())
	if err != nil {
		t.Fatalf("ApproveJoinRequest: %v", err)
	}
	if approved.Status() != domain.StatusActive || approved.Version().Int64() != 1 {
		t.Fatalf("approved membership = %+v", approved)
	}

	_, err = memberships.ApproveJoinRequest(ctx, pending.ID().String())
	assertCode(t, err, CodeConflict)

	promoted, err := memberships.ChangeRole(ctx, pending.ID().String(), "admin")
	if err != nil {
		t.Fatalf("ChangeRole: %v", err)
	}
	if promoted.Role() != domain.RoleAdmin || promoted.Version().Int64() != 2 {
		t.Fatalf("promoted membership = %+v", promoted)
	}

	left, err := memberships.LeaveTeam(ctx, pending.ID().String())
	if err != nil {
		t.Fatalf("LeaveTeam: %v", err)
	}
	if left.Status() != domain.StatusLeaved {
		t.Fatalf("status = %q", left.Status())
	}

	_, err = memberships.ChangeRole(ctx, pending.ID().String(), "OWNER")
	assertCode(t, err, CodeConflict)

	_, err = memberships.ChangeRole(ctx, pending.ID().String(), "GUEST")
	assertCode(t, err, CodeValidation)

	_, err = memberships.ApproveJoinRequest(ctx, "01ARZ3NDEKTSV4RRFFQ69G5FAV")
	assertCode(t, err, CodeNotFound)

	_, err = memberships.ChangeRole(ctx, "not-a-ulid", "ADMIN")
	assertCode(t, err, CodeValidation)
}

func TestMembershipExistenceChecks(t *testing.T) {
	ctx := context.Background()
	memberships, teams, users, _ := newMembershipUseCaseForTest()
	team := mustCreateTeam(t, ctx, teams, "Platform")

	_, err := memberships.JoinTeam(ctx, JoinTeamInput{
		TeamID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", UserID: "01ARZ3NDEKTSV4RRFFQ69G5FAW",
	})
	assertCode(t, err, CodeNotFound)

	user := mustCreateUser(t, ctx, users, "Alice", "alice@example.com")
	_, err = memberships.JoinTeam(ctx, JoinTeamInput{
		TeamID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", UserID: user.ID().String(),
	})
	assertCode(t, err, CodeNotFound)

	_, err = memberships.RequestJoinTeam(ctx, JoinTeamInput{
		TeamID: team.ID().String(), UserID: "01ARZ3NDEKTSV4RRFFQ69G5FAW",
	})
	assertCode(t, err, CodeNotFound)

	_, err = memberships.ListTeamMemberships(ctx, "not-a-ulid")
	assertCode(t, err, CodeValidation)

	_, err = memberships.ListTeamMemberships(ctx, "01ARZ3NDEKTSV4RRFFQ69G5FAV")
	assertCode(t, err, CodeNotFound)
}

func mustCreateTeam(t *testing.T, ctx context.Context, teams *TeamUseCase, name string) *domain.Team {
	t.Helper()
	team, err := teams.CreateTeam(ctx, CreateTeamInput{Name: name})
	if err != nil {
		t.Fatalf("CreateTeam(%q): %v", name, err)
	}
	return team
}

func mustCreateUser(t *testing.T, ctx context.Context, users *UserUseCase, name, email string) *domain.User {
	t.Helper()
	user, err := users.CreateUser(ctx, CreateUserInput{DisplayName: name, Email: email})
	if err != nil {
		t.Fatalf("CreateUser(%q): %v", name, err)
	}
	return user
}
