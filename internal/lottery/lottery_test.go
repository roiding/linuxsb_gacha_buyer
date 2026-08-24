package lottery

import (
	"sync"
	"testing"
	"time"

	"gacha-buyer/internal/accounts"
	"gacha-buyer/internal/config"
	"gacha-buyer/internal/db"
	"gacha-buyer/internal/site"
)

type fakeClient struct {
	result site.ReplyResult
}

func (f *fakeClient) Reply(topicURL, message string) site.ReplyResult {
	return f.result
}

func (f *fakeClient) ReplyWithRetry(topicURL, message string) site.ReplyResult {
	return f.result
}

type fakeManager struct {
	mu       sync.Mutex
	results  map[string]site.ReplyResult
	callArgs []config.SubAccount
}

func newFakeManager(results map[string]site.ReplyResult) *fakeManager {
	return &fakeManager{results: results}
}

func (f *fakeManager) Sub(sub config.SubAccount) (replier, *accounts.Acct, error) {
	f.mu.Lock()
	f.callArgs = append(f.callArgs, sub)
	f.mu.Unlock()

	res, ok := f.results[sub.Username]
	if ok {
		return &fakeClient{result: res}, &accounts.Acct{Username: sub.Username, Status: accounts.StatusOK, ID: 1}, nil
	}
	return &fakeClient{}, &accounts.Acct{Username: sub.Username, Status: accounts.StatusOK}, nil
}

type fakeDB struct {
	mu              sync.Mutex
	ids             map[string]int
	confirmed       map[string]bool
	pendingRecently map[string]bool
	addLogs         []db.LotteryReplyRow
}

func newFakeDB() *fakeDB {
	return &fakeDB{
		ids:             map[string]int{},
		confirmed:       map[string]bool{},
		pendingRecently: map[string]bool{},
		addLogs:         []db.LotteryReplyRow{},
	}
}

func (f *fakeDB) LotteryReplyConfirmed(accountID, topicID int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.confirmed[dbKey(accountID, topicID)]
}

func (f *fakeDB) LotteryReplyPendingRecently(accountID, topicID int, since time.Time) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pendingRecently[dbKey(accountID, topicID)]
}

func (f *fakeDB) AddLotteryReply(r *db.LotteryReplyRow) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addLogs = append(f.addLogs, *r)
	return nil
}

func (f *fakeDB) GetAccount(role, username string) (*db.AccountRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.ids[username]
	if !ok {
		id = len(f.ids) + 1
		f.ids[username] = id
	}
	return &db.AccountRow{ID: id, Username: username, Role: role}, nil
}

func (f *fakeDB) ListLotteryReplies(limit int) ([]*db.LotteryReplyRow, error) {
	return nil, nil
}

func dbKey(accountID, topicID int) string {
	return formatInt(accountID) + "|" + formatInt(topicID)
}

func formatInt(n int) string {
	if n <= 0 {
		return ""
	}
	return formatInt(n/10) + string(rune('0'+n%10))
}

func TestRunOnceSkipsConfirmedAndPending(t *testing.T) {
	d := newFakeDB()
	d.ids["u1"] = 1
	d.ids["u2"] = 2
	d.ids["u3"] = 3
	d.confirmed["1|1"] = true
	d.pendingRecently["2|1"] = true

	fm := newFakeManager(map[string]site.ReplyResult{
		"u3": {Submitted: true, Confirmed: true, ReplyID: 1, Message: "ok"},
	})

	cfg := config.Defaults()
	cfg.Site = "https://example.com"
	cfg.Lottery.URL = "https://example.com/topic/1"
	cfg.Lottery.Messages = []string{"m1"}
	cfg.DryRun = false
	cfg.Subs = []config.SubAccount{
		{Username: "u1", Enabled: true},
		{Username: "u2", Enabled: true},
		{Username: "u3", Enabled: true},
	}

	eng := NewWithMocks(&cfg, fm, d, t.Logf)
	eng.runOnce(make(chan struct{}))

	if len(d.addLogs) != 1 {
		t.Fatalf("confirmed/pending 账号应跳过，只期望 1 条记录，实际: %d", len(d.addLogs))
	}
	if len(fm.callArgs) != 1 || fm.callArgs[0].Username != "u3" {
		t.Fatalf("只应调用 u3: %+v", fm.callArgs)
	}
}

func TestRunOnceWritesRetryableResult(t *testing.T) {
	d := newFakeDB()
	fm := newFakeManager(map[string]site.ReplyResult{
		"u1": {Submitted: false, Retryable: true, Message: "提交过快，请等待验证码加载后再试"},
	})

	cfg := config.Defaults()
	cfg.Site = "https://example.com"
	cfg.Lottery.URL = "https://example.com/topic/1"
	cfg.Lottery.Messages = []string{"m1"}
	cfg.DryRun = false
	cfg.Subs = []config.SubAccount{
		{Username: "u1", Enabled: true},
	}

	eng := NewWithMocks(&cfg, fm, d, t.Logf)
	eng.runOnce(make(chan struct{}))

	if len(d.addLogs) != 1 {
		t.Fatalf("应记录一条回复: %d", len(d.addLogs))
	}
	if d.addLogs[0].Message != "提交过快，请等待验证码加载后再试" {
		t.Fatalf("应保留可重试错误消息: %s", d.addLogs[0].Message)
	}
}

func TestRunOnceStopsBetweenAccounts(t *testing.T) {
	d := newFakeDB()
	fm := newFakeManager(map[string]site.ReplyResult{
		"u1": {Submitted: true, Message: "ok"},
		"u2": {Submitted: true, Message: "ok"},
	})

	cfg := config.Defaults()
	cfg.Site = "https://example.com"
	cfg.Lottery.URL = "https://example.com/topic/1"
	cfg.Lottery.Messages = []string{"m1"}
	cfg.DryRun = false
	cfg.Subs = []config.SubAccount{
		{Username: "u1", Enabled: true},
		{Username: "u2", Enabled: true},
	}

	started := make(chan struct{})
	block := make(chan struct{})
	sleepCalls := 0
	sleepFn := func(d time.Duration) bool {
		sleepCalls++
		if sleepCalls == 1 {
			close(started)
			<-block
			return true
		}
		return true
	}

	eng := NewWithMocks(&cfg, fm, d, t.Logf)
	eng.SetSleep(sleepFn)

	go eng.StartOnce()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("任务未启动")
	}

	eng.Stop()
	close(block)

	done := make(chan struct{})
	go func() {
		for eng.Running() {
			time.Sleep(50 * time.Millisecond)
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop 后任务未结束")
	}

	fm.mu.Lock()
	calls := len(fm.callArgs)
	fm.mu.Unlock()
	if calls > 1 {
		t.Fatalf("Stop 后不应继续处理第二个账号，实际: %d", calls)
	}
}
