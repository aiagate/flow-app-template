package usecase

import (
	"context"
	"testing"
	"time"

	"github.com/aiagate/flow-app-template/internal/contract/port"
	"github.com/aiagate/flow-app-template/internal/domain"
)

func newBillingForTest(balances map[string]int64) (*BillingUseCase, *fakeGuard, *fakeQueue, *fakeQuotas, *fakeAudit) {
	guard := newFakeGuard(balances)
	queue := &fakeQueue{}
	quotas := &fakeQuotas{quotas: make(map[string]domain.AccountQuota)}
	audit := &fakeAudit{}
	return NewBillingUseCase(guard, quotas, audit, queue), guard, queue, quotas, audit
}

func TestGetAccountUsesHotPathThenRepository(t *testing.T) {
	ctx := context.Background()
	billing, _, _, quotas, _ := newBillingForTest(map[string]int64{"acct": 42})

	view, err := billing.GetAccount(ctx, "acct")
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if view.AccountID != "acct" || view.Balance != 42 || view.RateLimitRPM != 120 {
		t.Fatalf("view = %+v", view)
	}

	stored := time.Now().UTC().Add(-time.Hour)
	quotas.quotas["acct"] = domain.AccountQuota{
		AccountID: "acct", Balance: 1, RateLimitRPM: 10, UpdatedAt: stored,
	}
	view, err = billing.GetAccount(ctx, "acct")
	if err != nil {
		t.Fatalf("GetAccount with quota: %v", err)
	}
	if view.Balance != 42 {
		t.Fatalf("hot-path balance = %d, want 42", view.Balance)
	}
	if !view.UpdatedAt.Equal(stored) {
		t.Fatalf("updated_at = %s, want %s", view.UpdatedAt, stored)
	}

	quotas.quotas["persisted"] = domain.AccountQuota{
		AccountID: "persisted", Balance: 7, RateLimitRPM: 60, UpdatedAt: stored,
	}
	view, err = billing.GetAccount(ctx, "persisted")
	if err != nil {
		t.Fatalf("GetAccount(persisted): %v", err)
	}
	if view.Balance != 7 || view.RateLimitRPM != 60 {
		t.Fatalf("persisted view = %+v", view)
	}

	_, err = billing.GetAccount(ctx, "missing")
	assertCode(t, err, CodeNotFound)

	_, err = billing.GetAccount(ctx, "  ")
	assertCode(t, err, CodeValidation)
}

func TestCreditAccountEnqueuesMovementAndQuota(t *testing.T) {
	ctx := context.Background()
	billing, guard, queue, _, _ := newBillingForTest(map[string]int64{"acct": 100})

	view, err := billing.CreditAccount(ctx, CreditAccountInput{
		AccountID: " acct ", Amount: 50, Reason: " top-up ", RequestID: "req-1",
	})
	if err != nil {
		t.Fatalf("CreditAccount: %v", err)
	}
	if view.Balance != 150 {
		t.Fatalf("balance = %d, want 150", view.Balance)
	}
	if len(queue.billing) != 1 {
		t.Fatalf("billing events = %d, want 1", len(queue.billing))
	}
	event := queue.billing[0]
	if event.AccountID != "acct" || event.Amount != 50 || event.Reason != "top-up" || event.RequestID != "req-1" {
		t.Fatalf("billing event = %+v", event)
	}
	if event.ID.IsZero() {
		t.Fatal("billing event has a zero ID")
	}
	if len(queue.quotas) != 1 || queue.quotas[0].Balance != 150 || queue.quotas[0].AccountID != "acct" {
		t.Fatalf("quota syncs = %+v", queue.quotas)
	}
	if state, _ := guard.GetAccountState("acct"); state.Balance != 150 {
		t.Fatalf("guard balance = %d", state.Balance)
	}
}

func TestCreditAccountRollsBackWhenTheQueueRejects(t *testing.T) {
	ctx := context.Background()
	billing, guard, queue, _, _ := newBillingForTest(map[string]int64{"acct": 100})
	queue.err = port.ErrQueueFull

	_, err := billing.CreditAccount(ctx, CreditAccountInput{AccountID: "acct", Amount: 25, Reason: "top-up"})
	assertCode(t, err, CodeServiceUnavailable)

	state, _ := guard.GetAccountState("acct")
	if state.Balance != 100 {
		t.Fatalf("balance = %d, want the credit rolled back to 100", state.Balance)
	}
	if len(queue.billing) != 0 || len(queue.quotas) != 0 {
		t.Fatalf("failed credit persisted billing=%d quotas=%d", len(queue.billing), len(queue.quotas))
	}
}

func TestChargeAccountDebitsExactlyOnce(t *testing.T) {
	ctx := context.Background()
	billing, guard, queue, _, _ := newBillingForTest(map[string]int64{"acct": 100})

	view, err := billing.ChargeAccount(ctx, ChargeAccountInput{
		AccountID: "acct", Amount: 30, Reason: "manual adjustment", RequestID: "req-2",
	})
	if err != nil {
		t.Fatalf("ChargeAccount: %v", err)
	}
	if view.Balance != 70 {
		t.Fatalf("balance = %d, want 70", view.Balance)
	}
	if state, _ := guard.GetAccountState("acct"); state.Balance != 70 {
		t.Fatalf("guard balance = %d, want 70", state.Balance)
	}
	if len(queue.billing) != 1 || queue.billing[0].Amount != -30 {
		t.Fatalf("billing events = %+v", queue.billing)
	}
	if len(queue.quotas) != 1 || queue.quotas[0].Balance != 70 {
		t.Fatalf("quota syncs = %+v", queue.quotas)
	}
}

func TestChargeAccountInsufficientBalanceAndValidation(t *testing.T) {
	ctx := context.Background()
	billing, guard, queue, _, _ := newBillingForTest(map[string]int64{"acct": 10})

	_, err := billing.ChargeAccount(ctx, ChargeAccountInput{AccountID: "acct", Amount: 11, Reason: "too much"})
	assertCode(t, err, CodeInsufficientBalance)
	if state, _ := guard.GetAccountState("acct"); state.Balance != 10 {
		t.Fatalf("failed charge changed balance to %d", state.Balance)
	}
	if len(queue.billing) != 0 {
		t.Fatalf("failed charge enqueued %d events", len(queue.billing))
	}

	cases := []ChargeAccountInput{
		{AccountID: "acct", Amount: 0, Reason: "zero"},
		{AccountID: "acct", Amount: -1, Reason: "negative"},
		{AccountID: "acct", Amount: 1, Reason: "   "},
		{AccountID: "  ", Amount: 1, Reason: "no account"},
	}
	for _, input := range cases {
		_, err := billing.ChargeAccount(ctx, input)
		assertCode(t, err, CodeValidation)
	}

	longReason := ""
	for range 201 {
		longReason += "x"
	}
	_, err = billing.ChargeAccount(ctx, ChargeAccountInput{AccountID: "acct", Amount: 1, Reason: longReason})
	assertCode(t, err, CodeValidation)
}

func TestChargeAccountRefundsWhenTheQueueRejects(t *testing.T) {
	ctx := context.Background()
	billing, guard, queue, _, _ := newBillingForTest(map[string]int64{"acct": 50})
	queue.err = port.ErrQueueClosed

	_, err := billing.ChargeAccount(ctx, ChargeAccountInput{AccountID: "acct", Amount: 20, Reason: "charge"})
	assertCode(t, err, CodeServiceUnavailable)

	state, _ := guard.GetAccountState("acct")
	if state.Balance != 50 {
		t.Fatalf("balance = %d, want the charge refunded to 50", state.Balance)
	}
}

func TestChargeAccountUnknownAccountIsNotFound(t *testing.T) {
	ctx := context.Background()
	billing, _, _, _, _ := newBillingForTest(map[string]int64{"acct": 100})

	_, err := billing.ChargeAccount(ctx, ChargeAccountInput{AccountID: "missing", Amount: 1, Reason: "charge"})
	assertCode(t, err, CodeNotFound)
}

func TestBillingRejectsInvalidAccountIDs(t *testing.T) {
	ctx := context.Background()
	billing, _, _, _, _ := newBillingForTest(map[string]int64{"acct": 100})

	if _, err := billing.GetAccount(ctx, "bad account"); err == nil {
		t.Fatal("GetAccount accepted an account ID with a space")
	} else {
		assertCode(t, err, CodeValidation)
	}
	if _, err := billing.CreditAccount(ctx, CreditAccountInput{AccountID: "acct\nid", Amount: 1, Reason: "top-up"}); err == nil {
		t.Fatal("CreditAccount accepted an account ID with a newline")
	} else {
		assertCode(t, err, CodeValidation)
	}
	if _, err := billing.ChargeAccount(ctx, ChargeAccountInput{AccountID: "acct/id", Amount: 1, Reason: "charge"}); err == nil {
		t.Fatal("ChargeAccount accepted an account ID with a slash")
	} else {
		assertCode(t, err, CodeValidation)
	}
	if _, err := billing.ListBillingEvents(ctx, "acct@id", 10); err == nil {
		t.Fatal("ListBillingEvents accepted an account ID with an at sign")
	} else {
		assertCode(t, err, CodeValidation)
	}
	if _, err := billing.ListBillingEvents(ctx, "   ", 10); err != nil {
		t.Fatalf("ListBillingEvents rejected an empty filter: %v", err)
	}
}

func TestListBillingEventsAndRequestLogs(t *testing.T) {
	ctx := context.Background()
	billing, _, _, _, audit := newBillingForTest(nil)
	audit.events = []domain.BillingEvent{{ID: domain.NewEventID(), AccountID: "acct", Amount: 3}}
	audit.logs = []domain.RequestLog{{ID: "log", Path: "/api/v1/users"}}

	events, err := billing.ListBillingEvents(ctx, " acct ", 10)
	if err != nil {
		t.Fatalf("ListBillingEvents: %v", err)
	}
	if len(events) != 1 || events[0].Amount != 3 {
		t.Fatalf("events = %+v", events)
	}
	logs, err := billing.ListRequestLogs(ctx, 10)
	if err != nil {
		t.Fatalf("ListRequestLogs: %v", err)
	}
	if len(logs) != 1 || logs[0].Path != "/api/v1/users" {
		t.Fatalf("logs = %+v", logs)
	}

	audit.err = port.ErrNotFound
	_, err = billing.ListBillingEvents(ctx, "", 10)
	assertCode(t, err, CodeNotFound)
	_, err = billing.ListRequestLogs(ctx, 10)
	assertCode(t, err, CodeNotFound)
}
