package repo

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gitea.swiftstrike.ai/swiftstrike/tugboat/internal/config"
	"gitea.swiftstrike.ai/swiftstrike/tugboat/internal/remote"
)

// observedOutput lets tests inspect output before the command returns.
type observedOutput struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	changed chan struct{}
}

func newObservedOutput() *observedOutput {
	return &observedOutput{changed: make(chan struct{}, 1)}
}

func (o *observedOutput) Write(b []byte) (int, error) {
	o.mu.Lock()
	n, err := o.buf.Write(b)
	o.mu.Unlock()
	select {
	case o.changed <- struct{}{}:
	default:
	}
	return n, err
}

func (o *observedOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.String()
}

func (o *observedOutput) waitFor(t *testing.T, text string) {
	t.Helper()
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()
	for {
		if strings.Contains(o.String(), text) {
			return
		}
		select {
		case <-o.changed:
		case <-timeout.C:
			t.Fatalf("timed out waiting for %q; output:\n%s", text, o.String())
		}
	}
}

func observeCommand(t *testing.T, run func() error, inspect func(*observedOutput)) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = writer
	output := newObservedOutput()
	drained := make(chan struct{})
	go func() {
		_, _ = io.Copy(output, reader)
		close(drained)
	}()
	done := make(chan struct{})
	var runErr error
	go func() {
		runErr = run()
		_ = writer.Close()
		close(done)
	}()
	// inspect must release its gates even when an assertion fails.
	defer func() {
		<-done
		<-drained
		os.Stdout = original
		_ = reader.Close()
	}()
	inspect(output)
	<-done
	<-drained
	if runErr != nil {
		t.Fatalf("command failed: %v", runErr)
	}
	return output.String()
}

// Gate only the selected Git operation; all other Git commands remain real.
func gateGit(t *testing.T, repoPath, command string) func() {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	gate := filepath.Join(dir, "release")
	script := `#!/bin/sh
if [ "$PWD" = "$PROGRESS_REPO" ] && [ "$1" = "$PROGRESS_COMMAND" ]; then
    while [ ! -f "$PROGRESS_GATE" ]; do sleep 0.01; done
fi
exec "$PROGRESS_REAL_GIT" "$@"
`
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROGRESS_REAL_GIT", realGit)
	t.Setenv("PROGRESS_REPO", repoPath)
	t.Setenv("PROGRESS_COMMAND", command)
	t.Setenv("PROGRESS_GATE", gate)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	release := func() {
		if err := os.WriteFile(gate, nil, 0600); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(release)
	return release
}

type gatedMetadataClient struct {
	remote.Client
	release <-chan struct{}
}

func (c gatedMetadataClient) ListOrgRepos(org string) ([]remote.Repository, error) {
	<-c.release
	return c.Client.ListOrgRepos(org)
}

func TestVerboseScanProgressArrivesBeforeOtherRepositoriesFinish(t *testing.T) {
	base := t.TempDir()
	slow := createTestRepo(t, base, "acme", "slow", "main", filepath.Join(base, "slow"))
	fast := createTestRepo(t, base, "acme", "fast", "main", filepath.Join(base, "fast"))
	manager := newTestManager([]config.Target{repoTarget(slow), repoTarget(fast)}, fakeClientForRepos(slow, fast))
	manager.Verbose = true
	release := gateGit(t, slow.workPath, "fetch")
	output := observeCommand(t, func() error { return manager.Pull(nil, 2) }, func(out *observedOutput) {
		defer release()
		out.waitFor(t, "Checking 2 repositories...")
		out.waitFor(t, "[1/2] Checked "+fast.workPath)
		out.waitFor(t, "[1/2] [OK]    "+fast.workPath)
		if strings.Contains(out.String(), "Checked "+slow.workPath) {
			t.Fatalf("blocked scan was reported complete:\n%s", out.String())
		}
	})
	assertProgressCounts(t, output, 2, true)
}

func TestVerboseReportsFetchBeforeItFinishes(t *testing.T) {
	base := t.TempDir()
	repository := createTestRepo(t, base, "acme", "app", "main", filepath.Join(base, "app"))
	manager := newTestManager([]config.Target{repoTarget(repository)}, fakeClientForRepos(repository))
	manager.Verbose = true
	release := gateGit(t, repository.workPath, "fetch")
	output := observeCommand(t, func() error { return manager.Pull(nil, 1) }, func(out *observedOutput) {
		defer release()
		out.waitFor(t, "[DETAIL] "+repository.workPath+": git fetch")
		if strings.Contains(out.String(), "Checked ") {
			t.Fatalf("blocked fetch reported complete:\n%s", out.String())
		}
	})
	assertProgressCounts(t, output, 1, true)
}

func TestUpdateCommandsReportBeforeBlockedGitOperation(t *testing.T) {
	for _, command := range []string{"pull", "push", "sync"} {
		t.Run(command, func(t *testing.T) {
			base := t.TempDir()
			repo := createTestRepo(t, base, "acme", "app", "main", filepath.Join(base, "app"))
			gitCommand := "pull"
			if command == "pull" {
				seed := cloneRepo(t, repo.remotePath, filepath.Join(base, "seed"))
				commitFile(t, seed, "remote.txt", "remote work\n", "remote change")
				runGit(t, seed, "push", "origin", "main")
			}
			if command != "pull" {
				commitFile(t, repo.workPath, "local.txt", "work\n", "local change")
				gitCommand = "push"
			}
			manager := newTestManager([]config.Target{repoTarget(repo)}, fakeClientForRepos(repo))
			manager.Verbose = true
			release := gateGit(t, repo.workPath, gitCommand)
			output := observeCommand(t, func() error {
				switch command {
				case "pull":
					return manager.Pull(nil, 1)
				case "push":
					return manager.Push(nil, 1)
				default:
					return manager.Sync(nil, SyncOptions{Workers: 1})
				}
			}, func(out *observedOutput) {
				defer release()
				out.waitFor(t, "[START] "+repo.workPath+": "+command)
				if strings.Contains(out.String(), "[1/1] [") || strings.Contains(out.String(), "complete:") {
					t.Fatalf("blocked operation reported complete:\n%s", out.String())
				}
			})
			assertProgressCounts(t, output, 1, true)
		})
	}
}

func TestVerboseMetadataProgressArrivesBeforeProviderReturns(t *testing.T) {
	base := t.TempDir()
	repo := createTestRepo(t, base, "acme", "app", "main", filepath.Join(base, "app"))
	manager := newTestManager([]config.Target{repoTarget(repo)}, fakeClientForRepos(repo))
	manager.Verbose = true
	release := make(chan struct{})
	manager.providers["fake"] = gatedMetadataClient{Client: manager.providers["fake"], release: release}
	output := observeCommand(t, func() error { return manager.Push(nil, 1) }, func(out *observedOutput) {
		defer close(release)
		out.waitFor(t, "Loading repository metadata for fake/acme...")
		if strings.Contains(out.String(), "Metadata loaded") || strings.Contains(out.String(), "[START]") {
			t.Fatalf("blocked provider request reported complete:\n%s", out.String())
		}
	})
	if !strings.Contains(output, "Metadata loaded for fake/acme") || !strings.Contains(output, "nothing to push") {
		t.Fatalf("missing metadata or no-op result:\n%s", output)
	}
	assertProgressCounts(t, output, 1, true)
}

func assertProgressCounts(t *testing.T, output string, total int, verbose ...bool) {
	t.Helper()
	if strings.Contains(output, "[CHECK]") {
		t.Fatalf("duplicate check-start progress:\n%s", output)
	}
	patterns := []string{`(?m)^  \[([ \d]+)/(\d+)\] \[[A-Z]+\] [^\n]+$`}
	if len(verbose) > 0 && verbose[0] {
		patterns = append(patterns, `(?m)^  \[([ \d]+)/(\d+)\] Checked [^\n]+$`)
	} else if strings.Contains(output, "Checked ") || strings.Contains(output, "[DETAIL]") || strings.Contains(output, "[START]") {
		t.Fatalf("intermediate output without verbose flag:\n%s", output)
	}
	if len(verbose) == 0 || !verbose[0] {
		for _, marker := range []string{"Loading repository metadata", "Metadata loaded", "Metadata failed", "Reconciling remaining repository identities"} {
			if strings.Contains(output, marker) {
				t.Fatalf("verbose diagnostic without flag %q:\n%s", marker, output)
			}
		}
	}
	for _, pattern := range patterns {
		rows := regexp.MustCompile(pattern).FindAllStringSubmatch(output, -1)
		if len(rows) != total {
			t.Fatalf("got %d completions, want %d for %s:\n%s", len(rows), total, pattern, output)
		}
		for i, row := range rows {
			denominator, err := strconv.Atoi(row[2])
			expected := fmt.Sprintf("%*d", len(row[2]), i+1)
			if err != nil || row[1] != expected || denominator < i+1 {
				t.Fatalf("invalid completion %q:\n%s", row[0], output)
			}
			if i == len(rows)-1 && !strings.Contains(row[0], " Checked ") && denominator != total {
				t.Fatalf("final total %d, want %d:\n%s", denominator, total, output)
			}
		}
	}
}

func TestProgressReporterSerializesParallelLines(t *testing.T) {
	var output bytes.Buffer
	progress := &progressReporter{out: &output, verbose: true}
	const total = 100
	progress.beginChecks(total)
	var workers sync.WaitGroup
	for i := 0; i < total; i++ {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			path := fmt.Sprintf("/repos/%d", i)
			progress.checkFinished(path)
			progress.finish("  [OK]    %s: up to date\n", path)
		}(i)
	}
	workers.Wait()
	assertProgressCounts(t, output.String(), total, true)
	if lines := strings.Count(output.String(), "\n"); lines != 1+2*total {
		t.Fatalf("got %d lines, want %d", lines, 1+2*total)
	}
}

func TestUpdateProgressCountsSkipsFailuresAndMissingFoldouts(t *testing.T) {
	base := t.TempDir()
	parent := createTestRepo(t, base, "acme", "parent", "main", filepath.Join(base, "parent"))
	commitFile(t, parent.workPath, ".tugboat.json", `{"repos":[{"name":"acme/missing","target":"missing"}]}`, "add missing foldout")
	runGit(t, parent.workPath, "push", "origin", "main")
	dirty := createTestRepo(t, base, "acme", "dirty", "main", filepath.Join(base, "dirty"))
	writeFile(t, filepath.Join(dirty.workPath, "dirty.txt"), "local work")
	empty := createEmptyTestRepo(t, base, "acme", "empty", "main", filepath.Join(base, "empty"))
	archived := createTestRepo(t, base, "acme", "archived", "main", filepath.Join(base, "archived"))
	archived.archived = true
	broken := createTestRepo(t, base, "unavailable", "broken", "main", filepath.Join(base, "broken"))
	repos := []testRepo{parent, dirty, empty, archived, broken}
	var targets []config.Target
	for _, repo := range repos {
		targets = append(targets, repoTarget(repo))
	}
	client := fakeClientForRepos(repos...)
	client.listErr = map[string]error{"unavailable": fmt.Errorf("provider unavailable")}
	manager := newTestManager(targets, client)
	commands := []struct {
		name string
		run  func() error
	}{
		{"pull", func() error { return manager.Pull(nil, 3) }},
		{"push", func() error { return manager.Push(nil, 3) }},
		{"sync", func() error { return manager.Sync(nil, SyncOptions{Workers: 3}) }},
	}
	for _, command := range commands {
		t.Run(command.name, func(t *testing.T) {
			output := captureStdout(t, func() {
				if err := command.run(); err == nil {
					t.Fatal("expected provider failure")
				}
			})
			assertProgressCounts(t, output, 6)
			for _, text := range []string{
				"provider unavailable",
				"[ERROR] " + broken.workPath,
				"[SKIP]  " + archived.workPath,
				"[SKIP]  " + empty.workPath,
			} {
				if !strings.Contains(output, text) {
					t.Fatalf("missing %q:\n%s", text, output)
				}
			}
			if command.name == "sync" && !strings.Contains(output, "[OK]    "+parent.workPath+": up to date") {
				t.Fatalf("missing no-op sync result:\n%s", output)
			}
		})
	}
}

func TestEmptySelectionProgress(t *testing.T) {
	manager := newTestManager(nil, fakeClient{})
	for _, run := range []func() error{
		func() error { return manager.Pull(nil, 1) },
		func() error { return manager.Push(nil, 1) },
		func() error { return manager.Sync(nil, SyncOptions{Workers: 1}) },
	} {
		output := captureStdout(t, func() {
			if err := run(); err != nil {
				t.Fatal(err)
			}
		})
		if !strings.Contains(output, "discovering repositories...") {
			t.Fatalf("missing discovery message:\n%s", output)
		}
		assertProgressCounts(t, output, 0)
	}
}

func TestReadOnlyCommandsDoNotReportUpdateProgress(t *testing.T) {
	base := t.TempDir()
	repo := createTestRepo(t, base, "acme", "app", "main", filepath.Join(base, "app"))
	manager := newTestManager([]config.Target{repoTarget(repo)}, fakeClientForRepos(repo))
	for _, run := range []func() error{
		func() error { return manager.Status(nil, StatusOptions{Workers: 1}) },
		func() error { return manager.List(nil, false, 1) },
	} {
		output := captureStdout(t, func() {
			if err := run(); err != nil {
				t.Fatal(err)
			}
		})
		for _, marker := range []string{"discovering repositories", "[CHECK]", "Checked ", "Metadata loaded", "[START]"} {
			if strings.Contains(output, marker) {
				t.Fatalf("unexpected progress %q:\n%s", marker, output)
			}
		}
	}
}

func TestDefaultSyncUpdatesBeforeOtherFetchesFinish(t *testing.T) {
	for _, saved := range []bool{false, true} {
		t.Run(fmt.Sprint(saved), func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "work")
			if err := os.MkdirAll(root, 0755); err != nil {
				t.Fatal(err)
			}
			slow := createTestRepo(t, base, "acme", "slow", "main", filepath.Join(root, "slow"))
			fast := createTestRepo(t, base, "acme", "fast", "main", filepath.Join(root, "fast"))
			commitFile(t, fast.workPath, "local.txt", "local work", "local commit")
			want := gitText(t, fast.workPath, "rev-parse", "HEAD")
			targets := []config.Target{repoTarget(slow), repoTarget(fast)}
			if saved {
				targets = []config.Target{{Name: "acme", Provider: "fake", Org: "acme", Path: root}}
			}
			manager := newTestManager(targets, fakeClientForRepos(slow, fast))
			if saved {
				for _, r := range []testRepo{slow, fast} {
					if err := writeIdentity(r.workPath, newIdentity(manager.config.Providers["fake"], r.org, remoteRepo(r), r.remotePath)); err != nil {
						t.Fatal(err)
					}
				}
			}
			release := gateGit(t, slow.workPath, "fetch")
			output := observeCommand(t, func() error { return manager.Sync(nil, SyncOptions{Workers: 2}) }, func(out *observedOutput) {
				defer release()
				out.waitFor(t, "[1/2] [SYNC]  "+fast.workPath)
				if strings.Contains(out.String(), "[OK]    "+slow.workPath) || strings.Contains(out.String(), "complete:") {
					t.Fatalf("stalled repo reported complete: %s", out.String())
				}
				if got := gitText(t, fast.remotePath, "rev-parse", "main"); got != want {
					t.Fatalf("fast checkout only reported progress without pushing: got %s want %s", got, want)
				}
			})
			assertProgressCounts(t, output, 2)
		})
	}
}

// Gate a clone's provider verification while other clone workers proceed.
type gatedRepoClient struct {
	remote.Client
	name    string
	entered chan<- struct{}
	release <-chan struct{}
}

func (c gatedRepoClient) GetRepo(org, name string) (*remote.Repository, error) {
	if name == c.name {
		select {
		case c.entered <- struct{}{}:
		default:
		}
		<-c.release
	}
	return c.Client.GetRepo(org, name)
}

func TestMissingCloneResultArrivesBeforeOtherClonesFinish(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "work")
	slow := createTestRepo(t, base, "acme", "slow", "main", filepath.Join(base, "slow-seed"))
	fast := createTestRepo(t, base, "acme", "fast", "main", filepath.Join(base, "fast-seed"))
	manager := newTestManager([]config.Target{{Name: "acme", Provider: "fake", Org: "acme", Path: root}}, fakeClientForRepos(slow, fast))
	release, entered := make(chan struct{}), make(chan struct{}, 1)
	manager.providers["fake"] = gatedRepoClient{Client: manager.providers["fake"], name: "slow", release: release, entered: entered}
	output := observeCommand(t, func() error { return manager.Sync(nil, SyncOptions{Workers: 2}) }, func(out *observedOutput) {
		defer close(release)
		out.waitFor(t, "[1/2] [OK]    "+filepath.Join(root, "fast"))
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatal("slow clone was not started")
		}
		if isGitRepo(filepath.Join(root, "slow")) || strings.Contains(out.String(), "complete:") {
			t.Fatalf("stalled clone reported complete: %s", out.String())
		}
		id, err := readIdentity(filepath.Join(root, "fast"))
		if err != nil || id == nil || id.RepositoryID != remoteRepo(fast).ID {
			t.Fatalf("fast clone was not published and verified: %+v %v", id, err)
		}
	})
	assertProgressCounts(t, output, 2)
}

func TestProgressTotalAdjustsWithoutRepeatingResults(t *testing.T) {
	var output bytes.Buffer
	p := &progressReporter{out: &output}
	p.beginPlan([]string{"/fast", "/old", "/new", "/blocked"})
	p.finish("[OK] %s: done\n", "/fast")
	p.relocate("/old", "/new")
	p.forget("/blocked")
	p.finish("[OK] %s: renamed\n", "/new")
	p.plan("/foldout", "/foldout")
	p.forget("/fast") // Finished checkouts remain counted, including removals.
	p.finish("[OK] %s: cloned\n", "/foldout")
	for _, want := range []string{"[1/4] [OK] /fast", "[2/2] [OK] /new", "[3/3] [OK] /foldout"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("missing adjusted progress %q: %s", want, output.String())
		}
	}
	assertProgressCounts(t, output.String(), 3)
}

func TestSyncReportsWithdrawnMissingFoldout(t *testing.T) {
	base := t.TempDir()
	parent := createTestRepo(t, base, "acme", "parent", "main", filepath.Join(base, "parent"))
	child := createTestRepo(t, base, "acme", "child", "main", filepath.Join(base, "seed-child"))
	commitFile(t, parent.workPath, ".tugboat.json", `{"repos":[{"name":"acme/child","target":"child"}]}`, "declare foldout")
	runGit(t, parent.workPath, "push", "origin", "main")
	seed := cloneRepo(t, parent.remotePath, filepath.Join(base, "seed-parent"))
	commitFile(t, seed, ".tugboat.json", `{"repos":[]}`, "remove foldout")
	runGit(t, seed, "push", "origin", "main")
	m := newTestManager([]config.Target{repoTarget(parent)}, fakeClientForRepos(parent, child))
	output := captureStdout(t, func() {
		if err := m.Sync(nil, SyncOptions{Workers: 1}); err != nil {
			t.Fatal(err)
		}
	})
	assertProgressCounts(t, output, 2)
	if !strings.Contains(output, "[2/2] [SKIP]  "+filepath.Join(parent.workPath, "child")+": missing; foldout declaration removed by parent update") {
		t.Fatalf("missing terminal result for withdrawn foldout: %s", output)
	}
	if isGitRepo(filepath.Join(parent.workPath, "child")) {
		t.Fatal("withdrawn missing foldout was cloned")
	}
}
