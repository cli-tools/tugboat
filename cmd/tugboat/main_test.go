package main

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	"gitea.swiftstrike.ai/swiftstrike/tugboat/internal/config"
	"gitea.swiftstrike.ai/swiftstrike/tugboat/internal/repo"
)

func TestRefreshAppliesToEveryRepositoryCommand(t *testing.T) {
	for _, mode := range []string{"", "--pull", "--push", "--clone-only"} {
		base := []string{"t1", "--workers", "2"}
		if mode != "" {
			base = append([]string{mode}, base...)
		}
		for _, args := range [][]string{
			append([]string{"--refresh"}, base...),
			append(append([]string{}, base...), "--refresh"),
		} {
			refresh, remaining := parseRefresh(args)
			workers, remaining := parseWorkers(remaining)
			_, targets, err := parseSyncArgs(remaining)
			if !refresh || workers != 2 || err != nil || !reflect.DeepEqual(targets, []string{"t1"}) {
				t.Fatalf("parse sync %v: refresh=%v workers=%d targets=%v err=%v", args, refresh, workers, targets, err)
			}
		}
	}
	refresh, args := parseRefresh([]string{"--all", "t1", "--refresh", "-d"})
	debug, all, targets := parseStatusArgs(args)
	if !refresh || !debug || !all || !reflect.DeepEqual(targets, []string{"t1"}) {
		t.Fatal("status lost refresh or status options")
	}
	refresh, args = parseRefresh([]string{"t1", "--refresh", "-a"})
	if !refresh || !reflect.DeepEqual(args, []string{"t1", "-a"}) {
		t.Fatal("list lost refresh or list options")
	}
}

func TestCommandsShareDiscoveryCacheAndRefreshLiveMetadata(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/orgs/acme/repos" {
			http.NotFound(w, r)
			return
		}
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	}))
	defer server.Close()
	cfg := &config.Config{
		Providers: map[string]config.Provider{"gitea": {Type: "gitea", APIURL: server.URL, Token: "test-token"}},
		Targets:   []config.Target{{Name: "acme", Provider: "gitea", Org: "acme", Path: t.TempDir()}},
	}
	list := func(refresh bool) {
		t.Helper()
		m, err := newCommandManager(cfg, refresh, true)
		if err != nil {
			t.Fatal(err)
		}
		if err := m.List(nil, false, 1); err != nil {
			t.Fatal(err)
		}
	}
	list(false)
	list(false)
	if requests.Load() != 1 {
		t.Fatal("list did not reuse discovery cache")
	}
	for _, refresh := range []bool{false, true} {
		for _, mode := range []repo.SyncMode{repo.SyncBoth, repo.SyncPull, repo.SyncPush, repo.SyncCloneOnly} {
			before := requests.Load()
			m, err := newCommandManager(cfg, refresh, false)
			if err != nil {
				t.Fatal(err)
			}
			if m.Cache == nil || m.RefreshCache != refresh {
				t.Fatal("sync did not receive shared cache options")
			}
			if err := m.Sync(nil, repo.SyncOptions{Mode: mode, Workers: 1}); err != nil {
				t.Fatal(err)
			}
			if requests.Load() != before+1 {
				t.Fatalf("sync mode %d did not fetch live metadata", mode)
			}
			list(false)
			if requests.Load() != before+1 {
				t.Fatal("sync did not populate shared discovery cache")
			}
		}
		before := requests.Load()
		m, err := newCommandManager(cfg, refresh, false)
		if err != nil {
			t.Fatal(err)
		}
		if m.Cache == nil || m.RefreshCache != refresh {
			t.Fatal("status did not receive shared cache options")
		}
		if err := m.Status(nil, repo.StatusOptions{Workers: 1}); err != nil {
			t.Fatal(err)
		}
		list(false)
		if requests.Load() != before+1 {
			t.Fatal("status did not refresh the shared remote cache")
		}
	}
	before := requests.Load()
	list(true)
	if requests.Load() != before+1 {
		t.Fatal("list --refresh used cached metadata")
	}
}

func TestParseSyncArgs(t *testing.T) {
	opts, targets, err := parseSyncArgs([]string{"one", "--remove-archived", "two"})
	if err != nil || !opts.RemoveArchived {
		t.Fatal("--remove-archived was not parsed")
	}
	if want := []string{"one", "two"}; !reflect.DeepEqual(targets, want) {
		t.Fatalf("targets = %#v, want %#v", targets, want)
	}
}

func TestParseStatusArgs(t *testing.T) {
	debug, showAll, targets := parseStatusArgs([]string{"--all", "one", "-d", "two"})
	if !debug || !showAll {
		t.Fatalf("debug = %v, showAll = %v; want both true", debug, showAll)
	}
	if want := []string{"one", "two"}; !reflect.DeepEqual(targets, want) {
		t.Fatalf("targets = %#v, want %#v", targets, want)
	}
}

func TestParseVerbose(t *testing.T) {
	for _, args := range [][]string{
		{"--verbose", "t1", "--workers", "2"},
		{"t1", "--workers", "2", "--verbose"},
	} {
		verbose, remaining := parseVerbose(args)
		workers, targets := parseWorkers(remaining)
		if !verbose || workers != 2 || !reflect.DeepEqual(targets, []string{"t1"}) {
			t.Fatalf("parse %v: verbose=%v workers=%d targets=%v", args, verbose, workers, targets)
		}
	}
	verbose, targets := parseVerbose([]string{"t1"})
	if verbose || !reflect.DeepEqual(targets, []string{"t1"}) {
		t.Fatalf("unexpected default: %v %v", verbose, targets)
	}
}

func TestSyncDirectionsAndInvalidCombinations(t *testing.T) {
	for _, args := range [][]string{
		{"--pull", "--push"}, {"--clone-only", "--pull"}, {"--push", "--clone-only"},
		{"--push", "--remove-archived"}, {"--clone-only", "--remove-archived"},
		{"--pull", "-E"}, {"-a"}, {"--unknown"},
	} {
		if _, _, err := parseSyncArgs(args); err == nil {
			t.Errorf("accepted invalid options %v", args)
		}
	}
	for _, tc := range []struct {
		args []string
		mode repo.SyncMode
	}{
		{[]string{"t1"}, repo.SyncBoth}, {[]string{"--pull", "t1"}, repo.SyncPull},
		{[]string{"t1", "--push"}, repo.SyncPush}, {[]string{"--clone-only", "-E", "-a", "t1"}, repo.SyncCloneOnly},
	} {
		opts, targets, err := parseSyncArgs(tc.args)
		if err != nil || opts.Mode != tc.mode || !reflect.DeepEqual(targets, []string{"t1"}) {
			t.Fatalf("parse %v: %+v %v %v", tc.args, opts, targets, err)
		}
	}
}
