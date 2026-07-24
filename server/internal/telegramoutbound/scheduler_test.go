package telegramoutbound

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/time/rate"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// -- pure-function coverage ------------------------------------------------

func TestBackoff_MonotonicUntilCap(t *testing.T) {
	base, max := time.Second, 30*time.Second
	var prev time.Duration
	for i := int32(0); i < 10; i++ {
		d := backoff(i, base, max)
		if d < prev && d != max {
			t.Errorf("backoff(%d)=%v decreased below prev %v", i, d, prev)
		}
		if d > max {
			t.Errorf("backoff(%d)=%v exceeded max %v", i, d, max)
		}
		prev = d
	}
	// High retry count still caps at max.
	if got := backoff(1000, base, max); got != max {
		t.Errorf("backoff(1000)=%v, want max %v", got, max)
	}
	// Negative retry_count treated as 0.
	if got := backoff(-1, base, max); got != base {
		t.Errorf("backoff(-1)=%v, want base %v", got, base)
	}
}

func TestClassifyResponse_OKButBotSaysNotOK(t *testing.T) {
	// 200 status with ok:false → fatal (indicates a bad request the
	// retry loop cannot fix).
	res := classifyResponse(200, nil, false, "Bad Request: chat not found", 0, 0, 0)
	if res.Outcome != OutcomeFatal {
		t.Errorf("outcome=%v, want Fatal", res.Outcome)
	}
}

func TestClassifyResponse_ContextCancelIsTransient(t *testing.T) {
	res := classifyResponse(0, context.Canceled, false, "", 0, 0, 0)
	if res.Outcome != OutcomeTransient {
		t.Errorf("outcome=%v, want Transient", res.Outcome)
	}
}

func TestFmtUUID_InvalidReturnsPlaceholder(t *testing.T) {
	if got := fmtUUID(pgtype.UUID{}); got != "<nil>" {
		t.Errorf("fmtUUID(invalid)=%q, want <nil>", got)
	}
}

// -- Fakes ------------------------------------------------------------------

type fakeBot struct {
	mu           sync.Mutex
	sendCalls    []fakeSendCall
	createCalls  []fakeCreateCall
	sendResults  []SendResult
	createResults []SendResult
	sendIdx      int
	createIdx    int
}

type fakeSendCall struct {
	ChatID   int64
	ThreadID int
	Text     string
}
type fakeCreateCall struct {
	ChatID int64
	Name   string
}

func (f *fakeBot) SendMessage(_ context.Context, chatID int64, threadID int, text string) SendResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sendCalls = append(f.sendCalls, fakeSendCall{chatID, threadID, text})
	i := f.sendIdx
	f.sendIdx++
	if i >= len(f.sendResults) {
		return SendResult{Outcome: OutcomeOK, MessageID: 100 + i, StatusCode: 200}
	}
	return f.sendResults[i]
}

func (f *fakeBot) CreateForumTopic(_ context.Context, chatID int64, name string) SendResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls = append(f.createCalls, fakeCreateCall{chatID, name})
	i := f.createIdx
	f.createIdx++
	if i >= len(f.createResults) {
		return SendResult{Outcome: OutcomeOK, TopicID: 500 + i, StatusCode: 200}
	}
	return f.createResults[i]
}

// memQueries is a minimal SchedulerQueries fake. Only implements the
// behavior the scheduler exercises; every method logs a call for
// assertion.
type memQueries struct {
	mu sync.Mutex

	outbox  map[int64]db.TelegramOutbox
	threads map[[16]byte]db.TelegramThread
	issues  map[[16]byte]db.Issue
	spaces  map[[16]byte]db.Workspace
	nextID  int64

	// Assertion counters.
	claimCalls  int
	deleteCalls int
	bumpCalls   int
	failCalls   int
	rateCalls   int
	insertThreadCalls int
	deleteThreadCalls int
}

func newMemQueries() *memQueries {
	return &memQueries{
		outbox:  map[int64]db.TelegramOutbox{},
		threads: map[[16]byte]db.TelegramThread{},
		issues:  map[[16]byte]db.Issue{},
		spaces:  map[[16]byte]db.Workspace{},
	}
}

func (m *memQueries) enqueue(row db.TelegramOutbox) db.TelegramOutbox {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	row.ID = m.nextID
	if !row.CreatedAt.Valid {
		row.CreatedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	}
	if !row.NotBeforeAt.Valid {
		row.NotBeforeAt = pgtype.Timestamptz{Time: time.Now().Add(-time.Second), Valid: true}
	}
	m.outbox[row.ID] = row
	return row
}

func (m *memQueries) ClaimPendingTelegramOutbox(_ context.Context) ([]db.TelegramOutbox, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.claimCalls++
	now := time.Now()
	var claimed []db.TelegramOutbox
	for id, row := range m.outbox {
		if row.ClaimedAt.Valid || row.FailedAt.Valid {
			continue
		}
		if row.NotBeforeAt.Valid && row.NotBeforeAt.Time.After(now) {
			continue
		}
		row.ClaimedAt = pgtype.Timestamptz{Time: now, Valid: true}
		m.outbox[id] = row
		claimed = append(claimed, row)
	}
	return claimed, nil
}

func (m *memQueries) ResetStuckTelegramOutboxClaims(_ context.Context) (int64, error) {
	return 0, nil
}

func (m *memQueries) DeleteTelegramOutboxRow(_ context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleteCalls++
	delete(m.outbox, id)
	return nil
}

func (m *memQueries) BumpTelegramOutboxRetry(_ context.Context, arg db.BumpTelegramOutboxRetryParams) (db.TelegramOutbox, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bumpCalls++
	row, ok := m.outbox[arg.ID]
	if !ok {
		return db.TelegramOutbox{}, errors.New("not found")
	}
	row.RetryCount++
	row.ClaimedAt = pgtype.Timestamptz{}
	row.NotBeforeAt = arg.NotBeforeAt
	row.LastError = arg.LastError
	m.outbox[arg.ID] = row
	return row, nil
}

func (m *memQueries) ParkTelegramOutboxRateLimit(_ context.Context, arg db.ParkTelegramOutboxRateLimitParams) (db.TelegramOutbox, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rateCalls++
	row := m.outbox[arg.ID]
	row.ClaimedAt = pgtype.Timestamptz{}
	row.NotBeforeAt = arg.NotBeforeAt
	row.LastError = arg.LastError
	m.outbox[arg.ID] = row
	return row, nil
}

func (m *memQueries) ParkTelegramOutboxFailed(_ context.Context, arg db.ParkTelegramOutboxFailedParams) (db.TelegramOutbox, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failCalls++
	row := m.outbox[arg.ID]
	row.FailedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	row.ClaimedAt = pgtype.Timestamptz{}
	row.LastError = arg.LastError
	m.outbox[arg.ID] = row
	return row, nil
}

func (m *memQueries) GetTelegramThreadByIssue(_ context.Context, issueID pgtype.UUID) (db.TelegramThread, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.threads[issueID.Bytes]; ok {
		return t, nil
	}
	return db.TelegramThread{}, pgx.ErrNoRows
}

func (m *memQueries) InsertTelegramThread(_ context.Context, arg db.InsertTelegramThreadParams) (db.TelegramThread, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.insertThreadCalls++
	if existing, ok := m.threads[arg.IssueID.Bytes]; ok {
		return existing, nil
	}
	t := db.TelegramThread{
		IssueID:         arg.IssueID,
		ChatID:          arg.ChatID,
		MessageThreadID: arg.MessageThreadID,
		CreatedAt:       pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}
	m.threads[arg.IssueID.Bytes] = t
	return t, nil
}

func (m *memQueries) DeleteTelegramThreadByIssue(_ context.Context, issueID pgtype.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleteThreadCalls++
	delete(m.threads, issueID.Bytes)
	return nil
}

func (m *memQueries) GetIssue(_ context.Context, id pgtype.UUID) (db.Issue, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if is, ok := m.issues[id.Bytes]; ok {
		return is, nil
	}
	return db.Issue{}, pgx.ErrNoRows
}

func (m *memQueries) GetWorkspace(_ context.Context, id pgtype.UUID) (db.Workspace, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if w, ok := m.spaces[id.Bytes]; ok {
		return w, nil
	}
	return db.Workspace{}, pgx.ErrNoRows
}

// -- Test harness helpers ---------------------------------------------------

func mustUUID(hex string) pgtype.UUID {
	// hex is a 32-hex-char string; expand into 16 bytes.
	var b [16]byte
	for i := 0; i < 16; i++ {
		if _, err := parseHexByte(hex[2*i:2*i+2], &b[i]); err != nil {
			panic(err)
		}
	}
	return pgtype.UUID{Bytes: b, Valid: true}
}

func parseHexByte(s string, out *byte) (int, error) {
	if len(s) != 2 {
		return 0, errors.New("hex byte must be len 2")
	}
	hi, ok := hexNibble(s[0])
	if !ok {
		return 0, errors.New("bad hex")
	}
	lo, ok := hexNibble(s[1])
	if !ok {
		return 0, errors.New("bad hex")
	}
	*out = hi<<4 | lo
	return 1, nil
}

func hexNibble(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// seedIssue populates the mem-DB with an issue + its workspace so
// resolveThread's PUL-N lookup succeeds.
func (m *memQueries) seedIssue(issueID pgtype.UUID, number int32, prefix, title string) {
	wsID := mustUUID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	m.mu.Lock()
	m.issues[issueID.Bytes] = db.Issue{
		ID:          issueID,
		WorkspaceID: wsID,
		Title:       title,
		Number:      number,
	}
	m.spaces[wsID.Bytes] = db.Workspace{ID: wsID, IssuePrefix: prefix}
	m.mu.Unlock()
}

// permissiveLimiter — Inf rate so tests never sleep.
func permissiveLimiter() *Limiter {
	return newLimiterWithRates(rate.Inf, 1000, rate.Inf, 1000)
}

func newSchedForTest(t *testing.T, q SchedulerQueries, bot BotAPI) *Scheduler {
	t.Helper()
	s, err := NewScheduler(Config{
		Queries:      q,
		Client:       bot,
		Limiter:      permissiveLimiter(),
		ChatID:       -100123,
		TickInterval: time.Millisecond,
		MaxRetries:   3,
		BackoffBase:  10 * time.Millisecond,
		BackoffMax:   50 * time.Millisecond,
	}, nil)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	return s
}

// -- Scheduler behavior tests ----------------------------------------------

func mustPayload(t *testing.T, p outboxPayload) []byte {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return b
}

func TestScheduler_HappyPath_LazyCreatesTopicThenSends(t *testing.T) {
	q := newMemQueries()
	bot := &fakeBot{}
	sched := newSchedForTest(t, q, bot)

	issueID := mustUUID("11111111111111111111111111111111")
	q.seedIssue(issueID, 479, "PUL", "Telegram bridge")
	q.enqueue(db.TelegramOutbox{
		Kind:    "comment",
		IssueID: issueID,
		Payload: mustPayload(t, outboxPayload{Identifier: "PUL-479", AuthorLabel: "Vadim", Content: "hi from mem"}),
	})

	sched.tick(context.Background())

	if got := len(bot.createCalls); got != 1 {
		t.Fatalf("create called %d times, want 1", got)
	}
	if bot.createCalls[0].Name != "PUL-479 · Telegram bridge" {
		t.Errorf("create name=%q", bot.createCalls[0].Name)
	}
	if got := len(bot.sendCalls); got != 1 {
		t.Fatalf("send called %d times, want 1", got)
	}
	if bot.sendCalls[0].ThreadID != 500 {
		t.Errorf("thread_id=%d, want 500", bot.sendCalls[0].ThreadID)
	}
	if bot.sendCalls[0].Text != "PUL-479 · Vadim\n\nhi from mem" {
		t.Errorf("text=%q", bot.sendCalls[0].Text)
	}
	// Success → outbox row deleted.
	if got := len(q.outbox); got != 0 {
		t.Errorf("outbox not drained: %d rows left", got)
	}
	if _, ok := q.threads[issueID.Bytes]; !ok {
		t.Errorf("thread row not persisted")
	}
}

func TestScheduler_SecondSend_ReusesExistingTopic(t *testing.T) {
	q := newMemQueries()
	bot := &fakeBot{}
	sched := newSchedForTest(t, q, bot)
	issueID := mustUUID("22222222222222222222222222222222")
	q.seedIssue(issueID, 1, "PUL", "t")

	// Pre-seed thread as if a prior tick already created it.
	q.threads[issueID.Bytes] = db.TelegramThread{
		IssueID:         issueID,
		ChatID:          -100123,
		MessageThreadID: 77,
	}
	q.enqueue(db.TelegramOutbox{
		Kind:    "comment",
		IssueID: issueID,
		Payload: mustPayload(t, outboxPayload{Identifier: "PUL-1", AuthorLabel: "u", Content: "hey"}),
	})

	sched.tick(context.Background())

	if len(bot.createCalls) != 0 {
		t.Errorf("must NOT recreate topic; got %d", len(bot.createCalls))
	}
	if len(bot.sendCalls) != 1 || bot.sendCalls[0].ThreadID != 77 {
		t.Errorf("send should target existing thread 77; got %+v", bot.sendCalls)
	}
}

func TestScheduler_LongMessage_SplitsIntoChunks(t *testing.T) {
	q := newMemQueries()
	bot := &fakeBot{}
	sched := newSchedForTest(t, q, bot)
	issueID := mustUUID("33333333333333333333333333333333")
	q.seedIssue(issueID, 1, "PUL", "t")
	// Body ~10KB → 3 chunks after LF splitting.
	body := ""
	for i := 0; i < 500; i++ {
		body += "line abcdefghijklmnopqrstuvwxyz0123456789\n"
	}
	q.enqueue(db.TelegramOutbox{
		Kind:    "comment",
		IssueID: issueID,
		Payload: mustPayload(t, outboxPayload{Content: body}),
	})
	sched.tick(context.Background())
	if len(bot.sendCalls) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(bot.sendCalls))
	}
	// All chunks must have the (k/N) prefix.
	for i, call := range bot.sendCalls {
		if len(call.Text) < 5 || call.Text[:1] != "(" {
			t.Errorf("chunk %d missing (k/N) prefix: %q", i, call.Text[:min(20, len(call.Text))])
		}
	}
}

func TestScheduler_TopicDeleted_DeletesThreadAndReschedules(t *testing.T) {
	q := newMemQueries()
	bot := &fakeBot{
		sendResults: []SendResult{
			{Outcome: OutcomeTopicDeleted, StatusCode: 400, Description: "message thread not found"},
		},
	}
	sched := newSchedForTest(t, q, bot)
	issueID := mustUUID("44444444444444444444444444444444")
	q.seedIssue(issueID, 1, "PUL", "t")
	q.threads[issueID.Bytes] = db.TelegramThread{
		IssueID: issueID, ChatID: -100123, MessageThreadID: 999,
	}
	row := q.enqueue(db.TelegramOutbox{
		Kind: "comment", IssueID: issueID,
		Payload: mustPayload(t, outboxPayload{Content: "x"}),
	})
	sched.tick(context.Background())

	if _, ok := q.threads[issueID.Bytes]; ok {
		t.Errorf("stale thread must be deleted")
	}
	// Outbox row still present, rescheduled (not_before_at in the future).
	if updated, ok := q.outbox[row.ID]; !ok {
		t.Fatalf("outbox row should remain for retry")
	} else if !updated.NotBeforeAt.Time.After(time.Now()) {
		t.Errorf("not_before_at not advanced: %v", updated.NotBeforeAt.Time)
	}
	if q.deleteThreadCalls != 1 {
		t.Errorf("deleteThreadCalls=%d, want 1", q.deleteThreadCalls)
	}
}

func TestScheduler_RateLimit_ParksAndPreservesRetryCount(t *testing.T) {
	q := newMemQueries()
	bot := &fakeBot{
		sendResults: []SendResult{
			{Outcome: OutcomeRateLimit, RetryAfter: 5 * time.Second, StatusCode: 429},
		},
	}
	sched := newSchedForTest(t, q, bot)
	issueID := mustUUID("55555555555555555555555555555555")
	q.seedIssue(issueID, 1, "PUL", "t")
	q.threads[issueID.Bytes] = db.TelegramThread{IssueID: issueID, ChatID: -100123, MessageThreadID: 1}
	row := q.enqueue(db.TelegramOutbox{
		Kind: "comment", IssueID: issueID,
		Payload: mustPayload(t, outboxPayload{Content: "x"}),
	})
	sched.tick(context.Background())

	updated := q.outbox[row.ID]
	if updated.RetryCount != 0 {
		t.Errorf("rate-limit must NOT bump retry_count; got %d", updated.RetryCount)
	}
	if !updated.NotBeforeAt.Time.After(time.Now().Add(3 * time.Second)) {
		t.Errorf("not_before_at not advanced by retry_after; got %v", updated.NotBeforeAt.Time)
	}
}

func TestScheduler_FatalError_ParksAndFiresAlert(t *testing.T) {
	q := newMemQueries()
	bot := &fakeBot{
		sendResults: []SendResult{
			{Outcome: OutcomeFatal, StatusCode: 403, Description: "bot was kicked"},
		},
	}
	var alerted []db.TelegramOutbox
	sched, err := NewScheduler(Config{
		Queries:      q,
		Client:       bot,
		Limiter:      permissiveLimiter(),
		ChatID:       -100123,
		TickInterval: time.Millisecond,
		Alert: func(_ context.Context, row db.TelegramOutbox, _ SendResult) {
			alerted = append(alerted, row)
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	issueID := mustUUID("66666666666666666666666666666666")
	q.seedIssue(issueID, 1, "PUL", "t")
	q.threads[issueID.Bytes] = db.TelegramThread{IssueID: issueID, ChatID: -100123, MessageThreadID: 1}
	row := q.enqueue(db.TelegramOutbox{
		Kind: "comment", IssueID: issueID,
		Payload: mustPayload(t, outboxPayload{Content: "x"}),
	})
	sched.tick(context.Background())

	updated := q.outbox[row.ID]
	if !updated.FailedAt.Valid {
		t.Errorf("row should be parked failed_at not null")
	}
	if len(alerted) != 1 || alerted[0].ID != row.ID {
		t.Errorf("alert should fire once for parked row: %+v", alerted)
	}
}

func TestScheduler_TransientRetriesUntilMaxThenParks(t *testing.T) {
	q := newMemQueries()
	bot := &fakeBot{
		sendResults: []SendResult{
			{Outcome: OutcomeTransient, StatusCode: 502, Description: "bad gateway"},
			{Outcome: OutcomeTransient, StatusCode: 502},
			{Outcome: OutcomeTransient, StatusCode: 502},
		},
	}
	sched := newSchedForTest(t, q, bot) // MaxRetries=3 in helper
	issueID := mustUUID("77777777777777777777777777777777")
	q.seedIssue(issueID, 1, "PUL", "t")
	q.threads[issueID.Bytes] = db.TelegramThread{IssueID: issueID, ChatID: -100123, MessageThreadID: 1}
	row := q.enqueue(db.TelegramOutbox{
		Kind: "comment", IssueID: issueID,
		Payload: mustPayload(t, outboxPayload{Content: "x"}),
	})

	// Tick 1 — first attempt fails, retry_count=1, still eligible.
	sched.tick(context.Background())
	if updated := q.outbox[row.ID]; updated.RetryCount != 1 || updated.FailedAt.Valid {
		t.Errorf("after tick1: retry=%d failed=%v", updated.RetryCount, updated.FailedAt.Valid)
	}
	// Force eligibility for the next tick regardless of backoff parking.
	forceEligible(q, row.ID)

	sched.tick(context.Background())
	forceEligible(q, row.ID)

	sched.tick(context.Background())
	updated := q.outbox[row.ID]
	if !updated.FailedAt.Valid {
		t.Errorf("after tick3 with MaxRetries=3, should be parked; got retry=%d failed=%v",
			updated.RetryCount, updated.FailedAt.Valid)
	}
}

// forceEligible: rewrite not_before_at to the past so the mem-DB
// claim query picks the row back up on the next tick. Necessary
// because backoff parking in the scheduler advances not_before_at
// intentionally.
func forceEligible(q *memQueries, id int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	row := q.outbox[id]
	row.NotBeforeAt = pgtype.Timestamptz{Time: time.Now().Add(-time.Minute), Valid: true}
	q.outbox[id] = row
}

func TestScheduler_MalformedPayload_ParksAsFatal(t *testing.T) {
	q := newMemQueries()
	bot := &fakeBot{}
	sched := newSchedForTest(t, q, bot)
	issueID := mustUUID("88888888888888888888888888888888")
	q.seedIssue(issueID, 1, "PUL", "t")
	q.threads[issueID.Bytes] = db.TelegramThread{IssueID: issueID, ChatID: -100123, MessageThreadID: 1}
	// Non-JSON payload.
	row := q.enqueue(db.TelegramOutbox{Kind: "comment", IssueID: issueID, Payload: []byte("not json")})

	sched.tick(context.Background())

	if updated := q.outbox[row.ID]; !updated.FailedAt.Valid {
		t.Errorf("malformed payload row should be failed_at, got %+v", updated)
	}
	if len(bot.sendCalls) != 0 {
		t.Errorf("must not attempt Bot API for malformed payload")
	}
}

// TestNewScheduler_RequiresDeps checks the constructor guards.
func TestNewScheduler_RequiresDeps(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{"no queries", Config{Client: &fakeBot{}, ChatID: -1}},
		{"no client", Config{Queries: newMemQueries(), ChatID: -1}},
		{"no chat", Config{Queries: newMemQueries(), Client: &fakeBot{}}},
	}
	for _, tc := range cases {
		if _, err := NewScheduler(tc.cfg, nil); err == nil {
			t.Errorf("%s: expected error", tc.name)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
