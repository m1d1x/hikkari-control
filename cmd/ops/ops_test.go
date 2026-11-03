package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/m1d1x/hikkari-control/internal/support"
)

// hubFixture runs the real tester routes on an in-memory store, open gate.
func hubFixture(t *testing.T) (*client, *support.MemTesterStore) {
	t.Helper()
	store := support.NewMemTesterStore()
	mux := http.NewServeMux()
	support.RegisterTesterRoutes(mux, store, nil)
	support.RegisterTesterOpsRoutes(mux, store, func(_ string, h http.HandlerFunc) http.HandlerFunc { return h })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("HIKKARI_OPS_URL", srv.URL)
	t.Setenv("HIKKARI_OPS_TOKEN", "hko_test")
	return newClient(), store
}

func TestMCPToolsListAndLogsByAlias(t *testing.T) {
	c, store := hubFixture(t)
	ctx := context.Background()
	if err := store.AppendChunk(ctx, support.TesterChunkRequest{InstallID: "t-abc", Platform: "windows", Version: "1.0.24", Lines: []string{
		time.Now().Format("2006/01/02 15:04:05.000000") + " [singbox] ERROR i/o timeout",
	}}); err != nil {
		t.Fatal(err)
	}
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}
{"jsonrpc":"2.0","method":"notifications/initialized"}
{"jsonrpc":"2.0","id":2,"method":"tools/list"}
{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"logs","arguments":{"who":"001"}}}
{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"logs","arguments":{"who":"tester-404"}}}
`)
	var out bytes.Buffer
	if err := serveMCP(c, in, &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("a notification gets no answer, want 4 answers, got %d:\n%s", len(lines), out.String())
	}
	if !strings.Contains(lines[1], `"request_diag"`) {
		t.Fatalf("tools/list: %s", lines[1])
	}
	var call struct {
		Result struct {
			Content []struct{ Text string }
			IsError bool
		}
	}
	if err := json.Unmarshal([]byte(lines[2]), &call); err != nil || call.Result.IsError {
		t.Fatalf("logs by number: %v %s", err, lines[2])
	}
	if text := call.Result.Content[0].Text; !strings.Contains(text, `"alias":"tester-001"`) || !strings.Contains(text, "i/o timeout") {
		t.Fatalf("logs text: %s", text)
	}
	if !strings.Contains(lines[3], `"isError":true`) || !strings.Contains(lines[3], "not found") {
		t.Fatalf("unknown tester must be a tool error: %s", lines[3])
	}
}

// A diagnostic end to end against the real handlers: the client's next call
// takes the command, its result chunk acknowledges it, the tool returns the
// lines of that run and nothing else.
func TestDiagRoundTrip(t *testing.T) {
	c, store := hubFixture(t)
	ctx := context.Background()
	base := support.TesterChunkRequest{InstallID: "t-abc", Platform: "macos", Version: "1.0.24"}
	hb := base
	hb.Heartbeat = true
	if err := store.AppendChunk(ctx, hb); err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			cmds, _ := store.TakeCommands(ctx, "t-abc")
			if len(cmds) == 0 {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			res := base
			now := time.Now().Format("2006/01/02 15:04:05.000000")
			res.Lines = []string{
				now + " [doctor nonce=" + cmds[0].Nonce + "] proxy={\"enabled\":false}",
				now + " [singbox] an unrelated line",
			}
			_ = store.AppendChunk(ctx, res)
			_ = store.AckCommands(ctx, "t-abc", []string{cmds[0].Nonce})
			return
		}
	}()
	out, err := c.call("diag", map[string]string{"who": "tester-001"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `proxy={\"enabled\":false}`) || strings.Contains(out, "unrelated") {
		t.Fatalf("diag output: %s", out)
	}
}

func TestProbeSendsTargetsAndReadsItsLines(t *testing.T) {
	c, store := hubFixture(t)
	ctx := context.Background()
	if err := store.AppendChunk(ctx, support.TesterChunkRequest{InstallID: "t-abc", Platform: "windows", Version: "1.0.25", Heartbeat: true}); err != nil {
		t.Fatal(err)
	}
	go func() {
		for i := 0; i < 200; i++ {
			time.Sleep(20 * time.Millisecond)
			cmds, _ := store.TakeCommands(ctx, "t-abc")
			if len(cmds) == 0 {
				continue
			}
			if !strings.Contains(string(cmds[0].Params), "89.124.99.31:443") {
				t.Errorf("params: %s", cmds[0].Params)
			}
			now := time.Now().Format("2006/01/02 15:04:05.000000")
			_ = store.AppendChunk(ctx, support.TesterChunkRequest{InstallID: "t-abc", Platform: "windows", Version: "1.0.25", Lines: []string{
				now + " [probe nonce=" + cmds[0].Nonce + "] ams7-443 net=AS216071 flows[Ethernet]=0/12 med=-",
				now + " [singbox] an unrelated line",
			}})
			_ = store.AckCommands(ctx, "t-abc", []string{cmds[0].Nonce})
			return
		}
	}()
	out, err := c.call("probe", map[string]string{"who": "tester-001", "targets": "ams7-443=89.124.99.31:443"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "flows[Ethernet]=0/12") || strings.Contains(out, "unrelated") {
		t.Fatalf("probe output: %s", out)
	}
}

func TestLoadWaitsForTheRoundToClose(t *testing.T) {
	defer func(p time.Duration) { loadPoll = p }(loadPoll)
	loadPoll = 20 * time.Millisecond
	c, store := hubFixture(t)
	ctx := context.Background()
	base := support.TesterChunkRequest{InstallID: "t-abc", Platform: "windows", Version: "1.0.33"}
	hb := base
	hb.Heartbeat = true
	if err := store.AppendChunk(ctx, hb); err != nil {
		t.Fatal(err)
	}
	go func() {
		for i := 0; i < 200; i++ {
			time.Sleep(20 * time.Millisecond)
			cmds, _ := store.TakeCommands(ctx, "t-abc")
			if len(cmds) == 0 {
				continue
			}
			if cmds[0].Kind != "load_profiles" {
				t.Errorf("kind %q", cmds[0].Kind)
			}
			now := time.Now().Format("2006/01/02 15:04:05.000000")
			res := base
			res.Lines = []string{now + " [diag:load nonce=" + cmds[0].Nonce + "] started profiles=1"}
			_ = store.AppendChunk(ctx, res)
			_ = store.AckCommands(ctx, "t-abc", []string{cmds[0].Nonce})
			time.Sleep(100 * time.Millisecond)
			now = time.Now().Format("2006/01/02 15:04:05.000000")
			res.Lines = []string{
				now + " [diag:load] p-ams proto=vless-reality load_error=\"core did not start\"",
				now + " [singbox] an unrelated line",
				now + " [diag:load] on request: done",
			}
			_ = store.AppendChunk(ctx, res)
			return
		}
	}()
	out, err := c.call("load", map[string]string{"who": "tester-001"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "p-ams proto=vless-reality") || !strings.Contains(out, "on request: done") || strings.Contains(out, "unrelated") {
		t.Fatalf("load output: %s", out)
	}
}

func TestLoadRoundIsClosedOnlyByALogRecord(t *testing.T) {
	guide := `{"type":"meta","guide":{"sources":{"diag:load":"Раунд по запросу закрывает строка «on request: done»"}}}`
	if roundClosed(guide) {
		t.Fatal("the guide's description closed the round")
	}
	if !roundClosed(guide + "\n" + `{"type":"log","src":"diag:load","msg":"on request: done"}`) {
		t.Fatal("the closing record was missed")
	}
}

func TestLoadStopsAtARefusal(t *testing.T) {
	if !loadRefused.MatchString(`"msg":"error=\"a round is running\""`) {
		t.Fatal("a refusal was not seen")
	}
	if loadRefused.MatchString(`p-a proto=vless load_error=\"x\"`) {
		t.Fatal("a profile's load_error read as a refusal")
	}
}

func TestDiagRefusesAnInstallOffTheLine(t *testing.T) {
	c, store := hubFixture(t)
	req := support.TesterChunkRequest{InstallID: "t-off", Platform: "macos", Version: "1.0.24", Heartbeat: true}
	if err := store.AppendChunk(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	store.AgeInstallForTest("t-off", 10*time.Minute)
	_, err := c.call("diag", map[string]string{"who": "tester-001"})
	if err == nil || !strings.Contains(err.Error(), "не на связи") {
		t.Fatalf("want «не на связи» at once, got %v", err)
	}
}

func TestTrimExportKeepsHeadAndTail(t *testing.T) {
	data := []byte("meta\nsummary\na\nb\nc\nd\n")
	got := trimExport(data, 2)
	want := "meta\nsummary\n" + `{"type":"truncated","omitted_records":2,"hint":"сузь окно (from/to, last), отбери src или подними tail"}` + "\nc\nd"
	if got != want {
		t.Fatalf("got %q", got)
	}
}

func TestParseCLI(t *testing.T) {
	args, err := parseCLI("rename", []string{"003", "petya", "--note", "ноут"})
	if err != nil || args["who"] != "003" || args["alias"] != "petya" || args["note"] != "ноут" {
		t.Fatalf("%v %v", args, err)
	}
	if _, err := parseCLI("testers", []string{"extra"}); err == nil {
		t.Fatal("extra word must be refused")
	}
}
