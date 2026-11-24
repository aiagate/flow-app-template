package usecase

import (
	"context"
	"testing"
)

func newTeamUseCaseForTest() (*TeamUseCase, *fakeRepos) {
	repos := newFakeRepos()
	return NewTeamUseCase(repos, repos.Teams()), repos
}

func TestCreateAndReadTeams(t *testing.T) {
	ctx := context.Background()
	teams, _ := newTeamUseCaseForTest()

	created, err := teams.CreateTeam(ctx, CreateTeamInput{Name: "Platform"})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if created.ID().IsZero() || created.Version().Int64() != 0 {
		t.Fatalf("created team = %+v", created)
	}

	loaded, err := teams.GetTeam(ctx, created.ID().String())
	if err != nil {
		t.Fatalf("GetTeam: %v", err)
	}
	if loaded.Name().String() != "Platform" {
		t.Fatalf("name = %q", loaded.Name().String())
	}

	if _, err := teams.CreateTeam(ctx, CreateTeamInput{Name: "Engineering"}); err != nil {
		t.Fatalf("CreateTeam(second): %v", err)
	}
	list, err := teams.ListTeams(ctx)
	if err != nil {
		t.Fatalf("ListTeams: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("ListTeams length = %d, want 2", len(list))
	}
}

func TestTeamValidationAndNotFound(t *testing.T) {
	ctx := context.Background()
	teams, _ := newTeamUseCaseForTest()

	for _, name := range []string{"", " padded", "x"} {
		_, err := teams.CreateTeam(ctx, CreateTeamInput{Name: name})
		if name == "x" {
			if err != nil {
				t.Fatalf("CreateTeam(%q): %v", name, err)
			}
			continue
		}
		assertCode(t, err, CodeValidation)
	}

	_, err := teams.GetTeam(ctx, "not-a-ulid")
	assertCode(t, err, CodeValidation)

	_, err = teams.GetTeam(ctx, "01ARZ3NDEKTSV4RRFFQ69G5FAV")
	assertCode(t, err, CodeNotFound)
}

func TestUpdateTeamOptimisticLocking(t *testing.T) {
	ctx := context.Background()
	teams, _ := newTeamUseCaseForTest()
	created, err := teams.CreateTeam(ctx, CreateTeamInput{Name: "Platform"})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}

	zero := int64(0)
	updated, err := teams.UpdateTeam(ctx, UpdateTeamInput{
		TeamID:          created.ID().String(),
		Name:            "Engineering",
		ExpectedVersion: &zero,
	})
	if err != nil {
		t.Fatalf("UpdateTeam: %v", err)
	}
	if updated.Name().String() != "Engineering" || updated.Version().Int64() != 1 {
		t.Fatalf("updated team = %+v", updated)
	}

	_, err = teams.UpdateTeam(ctx, UpdateTeamInput{
		TeamID:          created.ID().String(),
		Name:            "Stale",
		ExpectedVersion: &zero,
	})
	assertCode(t, err, CodeConcurrencyConflict)

	one := int64(1)
	if _, err := teams.UpdateTeam(ctx, UpdateTeamInput{
		TeamID:          created.ID().String(),
		Name:            "Platform 2",
		ExpectedVersion: &one,
	}); err != nil {
		t.Fatalf("UpdateTeam(expected=1): %v", err)
	}

	loaded, err := teams.GetTeam(ctx, created.ID().String())
	if err != nil {
		t.Fatalf("GetTeam: %v", err)
	}
	if loaded.Name().String() != "Platform 2" || loaded.Version().Int64() != 2 {
		t.Fatalf("loaded team = %+v", loaded)
	}

	_, err = teams.UpdateTeam(ctx, UpdateTeamInput{
		TeamID: created.ID().String(),
		Name:   "No expectation",
	})
	if err != nil {
		t.Fatalf("UpdateTeam without expected version: %v", err)
	}

	negative := int64(-1)
	_, err = teams.UpdateTeam(ctx, UpdateTeamInput{
		TeamID: created.ID().String(), Name: "Bad", ExpectedVersion: &negative,
	})
	assertCode(t, err, CodeValidation)

	_, err = teams.UpdateTeam(ctx, UpdateTeamInput{TeamID: created.ID().String(), Name: ""})
	assertCode(t, err, CodeValidation)

	_, err = teams.UpdateTeam(ctx, UpdateTeamInput{
		TeamID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Name: "Missing",
	})
	assertCode(t, err, CodeNotFound)
}
