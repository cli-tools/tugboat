package repo

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gitea.swiftstrike.ai/swiftstrike/tugboat/internal/config"
	"gitea.swiftstrike.ai/swiftstrike/tugboat/internal/remote"
)

type fakeClient struct {
	repos   map[string]map[string]remote.Repository
	listErr map[string]error
	getErr  map[string]error
}

func (c fakeClient) ListOrgRepos(orgName string) ([]remote.Repository, error) {
	if err := c.listErr[orgName]; err != nil {
		return nil, err
	}
	reposByName := c.repos[orgName]
	repos := make([]remote.Repository, 0, len(reposByName))
	for _, repo := range reposByName {
		repos = append(repos, repo)
	}
	return repos, nil
}

func (c fakeClient) GetRepo(owner, repoName string) (*remote.Repository, error) {
	if err := c.getErr[owner+"/"+repoName]; err != nil {
		return nil, err
	}
	repo, ok := c.repos[owner][repoName]
	if !ok {
		return nil, nil
	}
	copy := repo
	return &copy, nil
}

type testRepo struct {
	org           string
	name          string
	defaultBranch string
	remotePath    string
	workPath      string
	empty         bool
	archived      bool
}

func TestGetCurrentBranchSupportsUnbornAndDetachedHEAD(t *testing.T) {
	base := t.TempDir()
	emptyRepo := createEmptyTestRepo(t, base, "acme", "empty", "main", filepath.Join(base, "empty-work"))

	branch, err := getCurrentBranch(emptyRepo.workPath)
	if err != nil {
		t.Fatalf("getCurrentBranch() on unborn branch error = %v", err)
	}
	if branch != "main" {
		t.Fatalf("getCurrentBranch() on unborn branch = %q, want %q", branch, "main")
	}

	repo := createTestRepo(t, base, "acme", "app", "main", filepath.Join(base, "app-work"))
	runGit(t, repo.workPath, "switch", "--detach")
	branch, err = getCurrentBranch(repo.workPath)
	if err != nil {
		t.Fatalf("getCurrentBranch() on detached HEAD error = %v", err)
	}
	if branch != "HEAD" {
		t.Fatalf("getCurrentBranch() on detached HEAD = %q, want %q", branch, "HEAD")
	}
}

func TestEmptyRepositoryIsReportedAndSkipped(t *testing.T) {
	base := t.TempDir()
	repo := createEmptyTestRepo(t, base, "acme", "empty", "main", filepath.Join(base, "empty-work"))
	manager := newTestManager([]config.Target{repoTarget(repo)}, fakeClientForRepos(repo))

	statusOutput := captureStdout(t, func() {
		if err := manager.Status(nil, StatusOptions{Workers: 1}); err != nil {
			t.Fatalf("Status() error = %v", err)
		}
	})
	if strings.Contains(statusOutput, "[ERROR]") {
		t.Fatalf("unexpected status error for empty repository:\n%s", statusOutput)
	}
	if !strings.Contains(statusOutput, "Empty (1)") || !strings.Contains(statusOutput, "EMPTY  .  main") {
		t.Fatalf("expected empty status, got:\n%s", statusOutput)
	}
	if !strings.Contains(statusOutput, "Summary: 1 repository: 0 clean, 1 empty") {
		t.Fatalf("expected empty summary count, got:\n%s", statusOutput)
	}

	commands := []struct {
		name string
		run  func() error
	}{
		{name: "Pull", run: func() error { return manager.Pull(nil, 1) }},
		{name: "Push", run: func() error { return manager.Push(nil, 1) }},
		{name: "Sync", run: func() error { return manager.Sync(nil, SyncOptions{Workers: 1}) }},
	}
	for _, command := range commands {
		t.Run(command.name, func(t *testing.T) {
			output := captureStdout(t, func() {
				if err := command.run(); err != nil {
					t.Fatalf("%s() error = %v", command.name, err)
				}
			})
			if !strings.Contains(output, "[SKIP]  "+repo.workPath+": no commits locally or on origin") {
				t.Fatalf("expected empty repository skip, got:\n%s", output)
			}
			if !strings.Contains(output, "0 failed") {
				t.Fatalf("expected no failures, got:\n%s", output)
			}
		})
	}
}

func TestSyncPullsFirstRemoteCommitIntoUnbornRepository(t *testing.T) {
	base := t.TempDir()
	repo := createEmptyTestRepo(t, base, "acme", "empty", "main", filepath.Join(base, "empty-work"))
	seed := cloneRepo(t, repo.remotePath, filepath.Join(base, "seed"))
	commitFile(t, seed, "README.md", "first\n", "first commit")
	runGit(t, seed, "push", "origin", "main")
	repo.empty = false

	manager := newTestManager([]config.Target{repoTarget(repo)}, fakeClientForRepos(repo))
	output := captureStdout(t, func() {
		if err := manager.Sync(nil, SyncOptions{Workers: 1}); err != nil {
			t.Fatalf("Sync() error = %v", err)
		}
	})

	if !strings.Contains(output, "[PULL]  "+repo.workPath+": 1 behind") {
		t.Fatalf("expected initial remote commit to be pulled, got:\n%s", output)
	}
	if data, err := os.ReadFile(filepath.Join(repo.workPath, "README.md")); err != nil || string(data) != "first\n" {
		t.Fatalf("README.md after sync = %q, %v; want %q", string(data), err, "first\\n")
	}
}

func TestPushSendsFirstLocalCommitToEmptyRemote(t *testing.T) {
	base := t.TempDir()
	repo := createEmptyTestRepo(t, base, "acme", "empty", "main", filepath.Join(base, "empty-work"))
	commitFile(t, repo.workPath, "README.md", "first\n", "first commit")

	manager := newTestManager([]config.Target{repoTarget(repo)}, fakeClientForRepos(repo))
	output := captureStdout(t, func() {
		if err := manager.Push(nil, 1); err != nil {
			t.Fatalf("Push() error = %v", err)
		}
	})

	if !strings.Contains(output, "[PUSH]  "+repo.workPath+": 1 commits") {
		t.Fatalf("expected initial local commit to be pushed, got:\n%s", output)
	}
	localHead := strings.TrimSpace(runGit(t, repo.workPath, "rev-parse", "HEAD"))
	remoteHead := strings.TrimSpace(runGit(t, repo.remotePath, "rev-parse", "refs/heads/main"))
	if localHead != remoteHead {
		t.Fatalf("remote HEAD = %q, want local HEAD %q", remoteHead, localHead)
	}
}

func TestPullSwitchesCleanPushedFeatureBranchToDefault(t *testing.T) {
	base := t.TempDir()
	repo := createTestRepo(t, base, "acme", "app", "main", filepath.Join(base, "app-work"))

	runGit(t, repo.workPath, "switch", "-c", "feature/clean")
	commitFile(t, repo.workPath, "feature.txt", "feature work\n", "feature commit")
	runGit(t, repo.workPath, "push", "-u", "origin", "feature/clean")

	manager := newTestManager([]config.Target{repoTarget(repo)}, fakeClientForRepos(repo))
	output := captureStdout(t, func() {
		if err := manager.Pull(nil, 1); err != nil {
			t.Fatalf("Pull() error = %v", err)
		}
	})

	if branch := currentBranch(t, repo.workPath); branch != "main" {
		t.Fatalf("current branch = %q, want %q", branch, "main")
	}
	if !strings.Contains(output, "[SWITCH] "+repo.workPath+": feature/clean -> main") {
		t.Fatalf("expected switch output, got:\n%s", output)
	}
}

func TestPullSkipsDirtyNonDefaultBranch(t *testing.T) {
	base := t.TempDir()
	repo := createTestRepo(t, base, "acme", "app", "main", filepath.Join(base, "app-work"))

	runGit(t, repo.workPath, "switch", "-c", "feature/dirty")
	writeFile(t, filepath.Join(repo.workPath, "dirty.txt"), "dirty\n")

	manager := newTestManager([]config.Target{repoTarget(repo)}, fakeClientForRepos(repo))
	output := captureStdout(t, func() {
		if err := manager.Pull(nil, 1); err != nil {
			t.Fatalf("Pull() error = %v", err)
		}
	})

	if branch := currentBranch(t, repo.workPath); branch != "feature/dirty" {
		t.Fatalf("current branch = %q, want %q", branch, "feature/dirty")
	}
	if !strings.Contains(output, "[SKIP]  "+repo.workPath+": on feature/dirty, dirty; not updating non-default branch") {
		t.Fatalf("expected dirty skip output, got:\n%s", output)
	}
}

func TestPullSkipsNonDefaultBranchWithLocalOnlyCommits(t *testing.T) {
	base := t.TempDir()
	repo := createTestRepo(t, base, "acme", "app", "main", filepath.Join(base, "app-work"))

	runGit(t, repo.workPath, "switch", "-c", "feature/ahead")
	commitFile(t, repo.workPath, "ahead.txt", "pushed\n", "pushed commit")
	runGit(t, repo.workPath, "push", "-u", "origin", "feature/ahead")
	commitFile(t, repo.workPath, "ahead.txt", "unpushed\n", "unpushed commit")

	manager := newTestManager([]config.Target{repoTarget(repo)}, fakeClientForRepos(repo))
	output := captureStdout(t, func() {
		if err := manager.Pull(nil, 1); err != nil {
			t.Fatalf("Pull() error = %v", err)
		}
	})

	if branch := currentBranch(t, repo.workPath); branch != "feature/ahead" {
		t.Fatalf("current branch = %q, want %q", branch, "feature/ahead")
	}
	if !strings.Contains(output, "[SKIP]  "+repo.workPath+": on feature/ahead, 1 ahead; not updating non-default branch") {
		t.Fatalf("expected ahead skip output, got:\n%s", output)
	}
}

func TestPullSkipsDirtyDefaultBranch(t *testing.T) {
	base := t.TempDir()
	repo := createTestRepo(t, base, "acme", "app", "main", filepath.Join(base, "app-work"))

	writeFile(t, filepath.Join(repo.workPath, "dirty.txt"), "dirty\n")

	manager := newTestManager([]config.Target{repoTarget(repo)}, fakeClientForRepos(repo))
	output := captureStdout(t, func() {
		if err := manager.Pull(nil, 1); err != nil {
			t.Fatalf("Pull() error = %v", err)
		}
	})

	if branch := currentBranch(t, repo.workPath); branch != "main" {
		t.Fatalf("current branch = %q, want %q", branch, "main")
	}
	if !strings.Contains(output, "[SKIP]  "+repo.workPath+": dirty") {
		t.Fatalf("expected dirty default-branch skip output, got:\n%s", output)
	}
}

func TestSyncSwitchesThenPullsDefaultBranch(t *testing.T) {
	base := t.TempDir()
	repo := createTestRepo(t, base, "acme", "app", "main", filepath.Join(base, "app-work"))

	runGit(t, repo.workPath, "switch", "-c", "feature/clean")
	commitFile(t, repo.workPath, "feature.txt", "feature work\n", "feature commit")
	runGit(t, repo.workPath, "push", "-u", "origin", "feature/clean")

	other := cloneRepo(t, repo.remotePath, filepath.Join(base, "other"))
	commitFile(t, other, "remote.txt", "from remote\n", "remote update")
	runGit(t, other, "push", "origin", "main")

	manager := newTestManager([]config.Target{repoTarget(repo)}, fakeClientForRepos(repo))
	output := captureStdout(t, func() {
		if err := manager.Sync(nil, SyncOptions{Workers: 1}); err != nil {
			t.Fatalf("Sync() error = %v", err)
		}
	})

	if branch := currentBranch(t, repo.workPath); branch != "main" {
		t.Fatalf("current branch = %q, want %q", branch, "main")
	}
	data, err := os.ReadFile(filepath.Join(repo.workPath, "remote.txt"))
	if err != nil {
		t.Fatalf("reading pulled file: %v", err)
	}
	if string(data) != "from remote\n" {
		t.Fatalf("remote.txt = %q, want %q", string(data), "from remote\n")
	}
	if !strings.Contains(output, "[SWITCH] "+repo.workPath+": feature/clean -> main") {
		t.Fatalf("expected switch output, got:\n%s", output)
	}
	if !strings.Contains(output, "[PULL]  "+repo.workPath+": 1 behind") {
		t.Fatalf("expected pull output, got:\n%s", output)
	}
}

func TestPullSwitchesWhenUpstreamGoneAndBranchContainedInDefault(t *testing.T) {
	base := t.TempDir()
	repo := createTestRepo(t, base, "acme", "app", "main", filepath.Join(base, "app-work"))

	runGit(t, repo.workPath, "switch", "-c", "feature/done")
	runGit(t, repo.workPath, "push", "-u", "origin", "feature/done")
	runGit(t, repo.workPath, "push", "origin", "--delete", "feature/done")
	runGit(t, repo.workPath, "update-ref", "-d", "refs/remotes/origin/feature/done")

	manager := newTestManager([]config.Target{repoTarget(repo)}, fakeClientForRepos(repo))
	output := captureStdout(t, func() {
		if err := manager.Pull(nil, 1); err != nil {
			t.Fatalf("Pull() error = %v", err)
		}
	})

	if branch := currentBranch(t, repo.workPath); branch != "main" {
		t.Fatalf("current branch = %q, want %q", branch, "main")
	}
	if !strings.Contains(output, "[SWITCH] "+repo.workPath+": feature/done -> main") {
		t.Fatalf("expected switch output, got:\n%s", output)
	}
}

func TestPullSkipsWhenUpstreamGoneAndBranchHasUnmergedCommits(t *testing.T) {
	base := t.TempDir()
	repo := createTestRepo(t, base, "acme", "app", "main", filepath.Join(base, "app-work"))

	runGit(t, repo.workPath, "switch", "-c", "feature/keep")
	commitFile(t, repo.workPath, "feature.txt", "feature work\n", "feature commit")
	runGit(t, repo.workPath, "push", "-u", "origin", "feature/keep")
	runGit(t, repo.workPath, "push", "origin", "--delete", "feature/keep")
	runGit(t, repo.workPath, "update-ref", "-d", "refs/remotes/origin/feature/keep")

	manager := newTestManager([]config.Target{repoTarget(repo)}, fakeClientForRepos(repo))
	output := captureStdout(t, func() {
		if err := manager.Pull(nil, 1); err != nil {
			t.Fatalf("Pull() error = %v", err)
		}
	})

	if branch := currentBranch(t, repo.workPath); branch != "feature/keep" {
		t.Fatalf("current branch = %q, want %q", branch, "feature/keep")
	}
	if !strings.Contains(output, "[SKIP]  "+repo.workPath+": on feature/keep, commits are not on main; not switching") {
		t.Fatalf("expected upstream-gone skip output, got:\n%s", output)
	}
}

func TestPullUsesRemoteDefaultBranchMetadataForCrossOrgFoldout(t *testing.T) {
	base := t.TempDir()
	parent := createTestRepo(t, base, "parentorg", "parent", "main", filepath.Join(base, "parent-work"))
	childPath := filepath.Join(parent.workPath, "child")
	child := createTestRepo(t, base, "otherorg", "child", "main", childPath)

	writeFile(t, filepath.Join(parent.workPath, ".gitignore"), "child/\n")
	writeFile(t, filepath.Join(parent.workPath, ".tugboat.json"), "{\n  \"repos\": [\n    { \"name\": \"otherorg/child\", \"target\": \"child\" }\n  ]\n}\n")
	runGit(t, parent.workPath, "add", ".gitignore", ".tugboat.json")
	runGit(t, parent.workPath, "commit", "-m", "add foldout")
	runGit(t, parent.workPath, "push", "origin", "main")

	runGit(t, child.workPath, "switch", "-c", "feature/foldout")
	commitFile(t, child.workPath, "feature.txt", "feature work\n", "feature commit")
	runGit(t, child.workPath, "push", "-u", "origin", "feature/foldout")
	runGit(t, child.workPath, "remote", "set-head", "origin", "-d")

	target := config.Target{
		Name:     "parent",
		Provider: "fake",
		Org:      parent.org,
		Repo:     parent.name,
		Path:     parent.workPath,
	}
	repos := fakeClient{
		repos: map[string]map[string]remote.Repository{
			parent.org: {
				parent.name: remoteRepo(parent),
			},
			child.org: {
				child.name: remoteRepo(child),
			},
		},
	}
	manager := newTestManager([]config.Target{target}, repos)
	output := captureStdout(t, func() {
		if err := manager.Pull(nil, 1); err != nil {
			t.Fatalf("Pull() error = %v", err)
		}
	})

	if branch := currentBranch(t, child.workPath); branch != "main" {
		t.Fatalf("child current branch = %q, want %q", branch, "main")
	}
	if !strings.Contains(output, "[SWITCH] "+child.workPath+": feature/foldout -> main") {
		t.Fatalf("expected foldout switch output, got:\n%s", output)
	}
}

func TestPullUsesCurrentBranchWhenDefaultBranchCannotBeResolved(t *testing.T) {
	base := t.TempDir()
	repo := createTestRepo(t, base, "acme", "app", "main", filepath.Join(base, "app-work"))

	other := cloneRepo(t, repo.remotePath, filepath.Join(base, "other"))
	commitFile(t, other, "remote.txt", "from remote\n", "remote update")
	runGit(t, other, "push", "origin", "main")

	runGit(t, repo.remotePath, "symbolic-ref", "HEAD", "refs/heads/missing")
	runGit(t, repo.workPath, "update-ref", "-d", "refs/remotes/origin/HEAD")

	target := repoTarget(repo)
	manager := newTestManager([]config.Target{target}, fakeClient{})
	output := captureStdout(t, func() {
		if err := manager.Pull(nil, 1); err != nil {
			t.Fatalf("Pull() error = %v", err)
		}
	})

	if branch := currentBranch(t, repo.workPath); branch != "main" {
		t.Fatalf("current branch = %q, want %q", branch, "main")
	}
	data, err := os.ReadFile(filepath.Join(repo.workPath, "remote.txt"))
	if err != nil {
		t.Fatalf("reading pulled file: %v", err)
	}
	if string(data) != "from remote\n" {
		t.Fatalf("remote.txt = %q, want %q", string(data), "from remote\n")
	}
	if !strings.Contains(output, "[PULL]  "+repo.workPath) {
		t.Fatalf("expected pull output, got:\n%s", output)
	}
}

func TestPullSkipsMissingRepoTargetPathButContinues(t *testing.T) {
	base := t.TempDir()
	repo := createTestRepo(t, base, "acme", "app", "main", filepath.Join(base, "app-work"))

	other := cloneRepo(t, repo.remotePath, filepath.Join(base, "other"))
	commitFile(t, other, "remote.txt", "from remote\n", "remote update")
	runGit(t, other, "push", "origin", "main")

	missing := config.Target{
		Name:     "missing",
		Provider: "fake",
		Org:      "acme",
		Repo:     "missing",
		Path:     filepath.Join(base, "missing-repo"),
	}
	manager := newTestManager([]config.Target{repoTarget(repo), missing}, fakeClientForRepos(repo))
	output := captureStdout(t, func() {
		if err := manager.Pull(nil, 1); err != nil {
			t.Fatalf("Pull() error = %v", err)
		}
	})

	data, err := os.ReadFile(filepath.Join(repo.workPath, "remote.txt"))
	if err != nil {
		t.Fatalf("reading pulled file: %v", err)
	}
	if string(data) != "from remote\n" {
		t.Fatalf("remote.txt = %q, want %q", string(data), "from remote\n")
	}
	if !strings.Contains(output, "[PULL]  "+repo.workPath) {
		t.Fatalf("expected pull output, got:\n%s", output)
	}
}

func TestStatusGroupsRepositoriesAndCollapsesCleanRows(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "target")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	archived := createTestRepo(t, base, "acme", "archived", "main", filepath.Join(root, "archived"))
	archived.archived = true
	writeFile(t, filepath.Join(archived.workPath, "archived-local.txt"), "local\n")
	dirty := createTestRepo(t, base, "acme", "dirty", "main", filepath.Join(root, "dirty"))
	writeFile(t, filepath.Join(dirty.workPath, "local.txt"), "local\n")
	clean := createTestRepo(t, base, "acme", "clean", "main", filepath.Join(root, "clean"))

	target := config.Target{Name: "target", Provider: "fake", Org: "acme", Path: root}
	manager := newTestManager([]config.Target{target}, fakeClientForRepos(archived, dirty, clean))
	output := captureStdout(t, func() {
		if err := manager.Status(nil, StatusOptions{Workers: 1}); err != nil {
			t.Fatalf("Status() error = %v", err)
		}
	})

	archivedAt := strings.Index(output, "Archived (1)")
	attentionAt := strings.Index(output, "Attention (1)")
	cleanAt := strings.Index(output, "Clean (1 hidden; use --all)")
	if archivedAt < 0 || attentionAt < archivedAt || cleanAt < attentionAt {
		t.Fatalf("status groups are missing or out of order:\n%s", output)
	}
	if !strings.Contains(output, "ARCHIVED  archived  main  dirty") || !strings.Contains(output, "DIRTY  dirty  main") {
		t.Fatalf("expected aligned relative-path rows, got:\n%s", output)
	}
	if strings.Contains(output, "CLEAN  clean") {
		t.Fatalf("clean row should be hidden by default:\n%s", output)
	}

	allOutput := captureStdout(t, func() {
		if err := manager.Status(nil, StatusOptions{ShowAll: true, Workers: 1}); err != nil {
			t.Fatalf("Status(--all) error = %v", err)
		}
	})
	if !strings.Contains(allOutput, "Clean (1)") || !strings.Contains(allOutput, "CLEAN  clean  main") {
		t.Fatalf("expected expanded clean group, got:\n%s", allOutput)
	}
}

func TestStatusReportsMissingConfiguredArchivedRepo(t *testing.T) {
	base := t.TempDir()
	repo := createTestRepo(t, base, "acme", "archived", "main", filepath.Join(base, "work"))
	repo.archived = true
	if err := os.RemoveAll(repo.workPath); err != nil {
		t.Fatal(err)
	}
	manager := newTestManager([]config.Target{repoTarget(repo)}, fakeClientForRepos(repo))

	output := captureStdout(t, func() {
		if err := manager.Status(nil, StatusOptions{Workers: 1}); err != nil {
			t.Fatalf("Status() error = %v", err)
		}
	})
	if !strings.Contains(output, "Archived (1)") || !strings.Contains(output, "ARCHIVED  .  main  missing") {
		t.Fatalf("expected missing archived status, got:\n%s", output)
	}
	if !strings.Contains(output, "1 archived, 0 orphan, 1 missing") {
		t.Fatalf("expected archived and missing summary counts, got:\n%s", output)
	}
}

func TestStatusReportsMissingConfiguredActiveRepo(t *testing.T) {
	base := t.TempDir()
	repo := createTestRepo(t, base, "acme", "active", "main", filepath.Join(base, "work"))
	if err := os.RemoveAll(repo.workPath); err != nil {
		t.Fatal(err)
	}
	manager := newTestManager([]config.Target{repoTarget(repo)}, fakeClientForRepos(repo))

	output := captureStdout(t, func() {
		if err := manager.Status(nil, StatusOptions{Workers: 1}); err != nil {
			t.Fatalf("Status() error = %v", err)
		}
	})
	if !strings.Contains(output, "Missing (1)") || !strings.Contains(output, "MISSING  .  main") {
		t.Fatalf("expected non-error missing status, got:\n%s", output)
	}
	if strings.Contains(output, "ERROR") {
		t.Fatalf("missing configured checkout should not be an error:\n%s", output)
	}
}

func TestUpdateCommandsSkipArchivedRepoWithoutRemovalFlag(t *testing.T) {
	base := t.TempDir()
	repo := createTestRepo(t, base, "acme", "archived", "main", filepath.Join(base, "work"))
	repo.archived = true
	manager := newTestManager([]config.Target{repoTarget(repo)}, fakeClientForRepos(repo))

	commands := []struct {
		name string
		run  func() error
	}{
		{name: "pull", run: func() error { return manager.Pull(nil, 1) }},
		{name: "push", run: func() error { return manager.Push(nil, 1) }},
		{name: "sync", run: func() error { return manager.Sync(nil, SyncOptions{Workers: 1}) }},
	}
	for _, command := range commands {
		output := captureStdout(t, func() {
			if err := command.run(); err != nil {
				t.Fatalf("%s error = %v", command.name, err)
			}
		})
		if !strings.Contains(output, "[SKIP]  "+repo.workPath+": archived") {
			t.Fatalf("expected archived skip from %s, got:\n%s", command.name, output)
		}
	}
	if !isGitRepo(repo.workPath) {
		t.Fatal("archived repository was removed without --remove-archived")
	}
}

func TestSyncRemovesCleanArchivedRepo(t *testing.T) {
	base := t.TempDir()
	repo := createTestRepo(t, base, "acme", "archived", "main", filepath.Join(base, "work"))
	repo.archived = true
	manager := newTestManager([]config.Target{repoTarget(repo)}, fakeClientForRepos(repo))

	output := captureStdout(t, func() {
		if err := manager.Sync(nil, SyncOptions{RemoveArchived: true, Workers: 1}); err != nil {
			t.Fatalf("Sync() error = %v", err)
		}
	})
	if _, err := os.Stat(repo.workPath); !os.IsNotExist(err) {
		t.Fatalf("archived checkout still exists: %v", err)
	}
	if !strings.Contains(output, "[REMOVE] "+repo.workPath) || !strings.Contains(output, "1 removed") {
		t.Fatalf("expected removal output, got:\n%s", output)
	}
}

func TestSyncRemovesEmptyArchivedRepo(t *testing.T) {
	base := t.TempDir()
	repo := createEmptyTestRepo(t, base, "acme", "empty", "main", filepath.Join(base, "work"))
	repo.archived = true
	manager := newTestManager([]config.Target{repoTarget(repo)}, fakeClientForRepos(repo))

	if err := manager.Sync(nil, SyncOptions{RemoveArchived: true, Workers: 1}); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if _, err := os.Stat(repo.workPath); !os.IsNotExist(err) {
		t.Fatalf("empty archived checkout still exists: %v", err)
	}
}

func TestSyncFastForwardsThenRemovesArchivedRepo(t *testing.T) {
	base := t.TempDir()
	repo := createTestRepo(t, base, "acme", "archived", "main", filepath.Join(base, "work"))
	other := cloneRepo(t, repo.remotePath, filepath.Join(base, "other"))
	commitFile(t, other, "remote.txt", "remote\n", "remote update")
	runGit(t, other, "push", "origin", "main")
	repo.archived = true
	manager := newTestManager([]config.Target{repoTarget(repo)}, fakeClientForRepos(repo))

	output := captureStdout(t, func() {
		if err := manager.Sync(nil, SyncOptions{RemoveArchived: true, Workers: 1}); err != nil {
			t.Fatalf("Sync() error = %v", err)
		}
	})
	if !strings.Contains(output, "[PULL]  "+repo.workPath+": fast-forwarded archived default branch") {
		t.Fatalf("expected archived fast-forward, got:\n%s", output)
	}
	if _, err := os.Stat(repo.workPath); !os.IsNotExist(err) {
		t.Fatalf("archived checkout still exists: %v", err)
	}
}

func TestSyncRemovesArchivedRepoFromFullyPushedFeatureBranch(t *testing.T) {
	base := t.TempDir()
	repo := createTestRepo(t, base, "acme", "archived", "main", filepath.Join(base, "work"))
	runGit(t, repo.workPath, "switch", "-c", "feature/pushed")
	commitFile(t, repo.workPath, "feature.txt", "pushed\n", "pushed feature")
	runGit(t, repo.workPath, "push", "-u", "origin", "feature/pushed")
	repo.archived = true
	manager := newTestManager([]config.Target{repoTarget(repo)}, fakeClientForRepos(repo))

	if err := manager.Sync(nil, SyncOptions{RemoveArchived: true, Workers: 1}); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if _, err := os.Stat(repo.workPath); !os.IsNotExist(err) {
		t.Fatalf("archived checkout still exists: %v", err)
	}
}

func TestArchivedRemovalAllowsIgnoredFilesButRejectsDirtyWork(t *testing.T) {
	t.Run("ignored files are disposable", func(t *testing.T) {
		base := t.TempDir()
		repo := createTestRepo(t, base, "acme", "archived", "main", filepath.Join(base, "work"))
		commitFile(t, repo.workPath, ".gitignore", "cache/\n", "ignore cache")
		runGit(t, repo.workPath, "push", "origin", "main")
		writeFile(t, filepath.Join(repo.workPath, "cache", "artifact.bin"), "ignored\n")
		repo.archived = true
		manager := newTestManager([]config.Target{repoTarget(repo)}, fakeClientForRepos(repo))

		if err := manager.Sync(nil, SyncOptions{RemoveArchived: true, Workers: 1}); err != nil {
			t.Fatalf("Sync() error = %v", err)
		}
		if _, err := os.Stat(repo.workPath); !os.IsNotExist(err) {
			t.Fatalf("checkout containing only ignored data still exists: %v", err)
		}
	})

	t.Run("untracked files block removal", func(t *testing.T) {
		base := t.TempDir()
		repo := createTestRepo(t, base, "acme", "archived", "main", filepath.Join(base, "work"))
		writeFile(t, filepath.Join(repo.workPath, "local.txt"), "local\n")
		repo.archived = true
		manager := newTestManager([]config.Target{repoTarget(repo)}, fakeClientForRepos(repo))

		output := captureStdout(t, func() {
			if err := manager.Sync(nil, SyncOptions{RemoveArchived: true, Workers: 1}); err != nil {
				t.Fatalf("safety skip should not fail Sync(): %v", err)
			}
		})
		if !strings.Contains(output, "archived, dirty worktree") || !isGitRepo(repo.workPath) {
			t.Fatalf("expected dirty checkout to be retained, got:\n%s", output)
		}
	})
}

func TestArchivedRemovalProtectsHiddenGitWork(t *testing.T) {
	tests := []struct {
		name       string
		prepare    func(t *testing.T, repo testRepo, base string)
		wantReason string
	}{
		{
			name: "other branch commit",
			prepare: func(t *testing.T, repo testRepo, _ string) {
				runGit(t, repo.workPath, "switch", "-c", "local-work")
				commitFile(t, repo.workPath, "local.txt", "local\n", "local work")
				runGit(t, repo.workPath, "switch", "main")
			},
			wantReason: "local-only commits",
		},
		{
			name: "stash",
			prepare: func(t *testing.T, repo testRepo, _ string) {
				writeFile(t, filepath.Join(repo.workPath, "README.md"), "stashed\n")
				runGit(t, repo.workPath, "stash", "push", "-m", "saved work")
			},
			wantReason: "local-only commits",
		},
		{
			name: "local tag commit",
			prepare: func(t *testing.T, repo testRepo, _ string) {
				runGit(t, repo.workPath, "switch", "-c", "tagged-work")
				commitFile(t, repo.workPath, "tagged.txt", "tagged\n", "tagged work")
				runGit(t, repo.workPath, "tag", "local-only")
				runGit(t, repo.workPath, "switch", "main")
				runGit(t, repo.workPath, "branch", "-D", "tagged-work")
			},
			wantReason: "local-only commits",
		},
		{
			name: "active operation",
			prepare: func(t *testing.T, repo testRepo, _ string) {
				writeFile(t, filepath.Join(repo.workPath, ".git", "BISECT_LOG"), "# active bisect\n")
			},
			wantReason: "active Git operation",
		},
		{
			name: "linked worktree",
			prepare: func(t *testing.T, repo testRepo, base string) {
				runGit(t, repo.workPath, "worktree", "add", "-b", "linked", filepath.Join(base, "linked"))
			},
			wantReason: "linked worktrees",
		},
		{
			name: "nested checkout",
			prepare: func(t *testing.T, repo testRepo, _ string) {
				writeFile(t, filepath.Join(repo.workPath, ".git", "info", "exclude"), "nested/\n")
				nested := filepath.Join(repo.workPath, "nested")
				if err := os.MkdirAll(nested, 0755); err != nil {
					t.Fatal(err)
				}
				runGit(t, nested, "init")
			},
			wantReason: "nested Git checkout",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base := t.TempDir()
			repo := createTestRepo(t, base, "acme", "archived", "main", filepath.Join(base, "work"))
			test.prepare(t, repo, base)
			repo.archived = true
			manager := newTestManager([]config.Target{repoTarget(repo)}, fakeClientForRepos(repo))

			output := captureStdout(t, func() {
				if err := manager.Sync(nil, SyncOptions{RemoveArchived: true, Workers: 1}); err != nil {
					t.Fatalf("safety skip should not fail Sync(): %v", err)
				}
			})
			if !strings.Contains(output, test.wantReason) || !isGitRepo(repo.workPath) {
				t.Fatalf("expected checkout to be retained for %s, got:\n%s", test.wantReason, output)
			}
		})
	}
}

func TestArchivedRemovalProcessesFoldoutsBeforeParent(t *testing.T) {
	base := t.TempDir()
	parent := createTestRepo(t, base, "parentorg", "parent", "main", filepath.Join(base, "parent-work"))
	child := createTestRepo(t, base, "childorg", "child", "main", filepath.Join(parent.workPath, "child"))
	writeFile(t, filepath.Join(parent.workPath, ".gitignore"), "child/\n")
	writeFile(t, filepath.Join(parent.workPath, ".tugboat.json"), "{\n  \"repos\": [{\"name\": \"childorg/child\", \"target\": \"child\"}]\n}\n")
	runGit(t, parent.workPath, "add", ".gitignore", ".tugboat.json")
	runGit(t, parent.workPath, "commit", "-m", "add foldout")
	runGit(t, parent.workPath, "push", "origin", "main")
	parent.archived = true
	child.archived = true

	target := repoTarget(parent)
	manager := newTestManager([]config.Target{target}, fakeClientForRepos(parent, child))
	output := captureStdout(t, func() {
		if err := manager.Sync(nil, SyncOptions{RemoveArchived: true, Workers: 1}); err != nil {
			t.Fatalf("Sync() error = %v", err)
		}
	})
	if _, err := os.Stat(parent.workPath); !os.IsNotExist(err) {
		t.Fatalf("parent checkout still exists: %v\n%s", err, output)
	}
	assertProgressCounts(t, output, 2)
	childResult := "[1/2] [REMOVE] " + child.workPath
	parentStart := "[START] " + parent.workPath + ":"
	parentResult := "[2/2] [REMOVE] " + parent.workPath
	if !strings.Contains(output, childResult) || !strings.Contains(output, parentResult) ||
		strings.Index(output, parentStart) < strings.Index(output, childResult) {
		t.Fatalf("cleanup progress must complete the child before starting the parent:\n%s", output)
	}
	if strings.Count(output, "[REMOVE]") != 2 {
		t.Fatalf("expected parent and child removal, got:\n%s", output)
	}
}

func TestArchivedRemovalOriginMismatchIsOperationalError(t *testing.T) {
	base := t.TempDir()
	repo := createTestRepo(t, base, "acme", "archived", "main", filepath.Join(base, "work"))
	repo.archived = true
	client := fakeClientForRepos(repo)
	remoteRepo := client.repos[repo.org][repo.name]
	remoteRepo.CloneURL = filepath.Join(base, "different.git")
	client.repos[repo.org][repo.name] = remoteRepo
	manager := newTestManager([]config.Target{repoTarget(repo)}, client)

	var syncErr error
	output := captureStdout(t, func() {
		syncErr = manager.Sync(nil, SyncOptions{RemoveArchived: true, Workers: 1})
	})
	if syncErr == nil || !strings.Contains(output, "does not match provider repository") {
		t.Fatalf("expected origin mismatch error, err=%v output:\n%s", syncErr, output)
	}
	assertProgressCounts(t, output, 1)
	if !isGitRepo(repo.workPath) {
		t.Fatal("origin-mismatched checkout was removed")
	}
}

func TestArchivedRemovalProviderFailureIsOperationalError(t *testing.T) {
	base := t.TempDir()
	repo := createTestRepo(t, base, "acme", "archived", "main", filepath.Join(base, "work"))
	repo.archived = true
	client := fakeClientForRepos(repo)
	client.listErr = map[string]error{"acme": errors.New("provider unavailable")}
	manager := newTestManager([]config.Target{repoTarget(repo)}, client)

	var syncErr error
	output := captureStdout(t, func() {
		syncErr = manager.Sync(nil, SyncOptions{RemoveArchived: true, Workers: 1})
	})
	if syncErr == nil || !strings.Contains(output, "provider unavailable") {
		t.Fatalf("expected provider metadata error, err=%v output:\n%s", syncErr, output)
	}
	assertProgressCounts(t, output, 1)
	if !isGitRepo(repo.workPath) {
		t.Fatal("checkout was removed without confirmed archive metadata")
	}
}

func newTestManager(targets []config.Target, client fakeClient) *Manager {
	cfg := &config.Config{
		Providers: map[string]config.Provider{
			"fake": {
				Type:    "github",
				APIURL:  "https://example.invalid",
				Options: config.ProviderOptions{},
			},
		},
		Targets: targets,
	}
	return NewManager(map[string]remote.Client{"fake": client}, cfg)
}

func repoTarget(repo testRepo) config.Target {
	return config.Target{
		Name:     repo.name,
		Provider: "fake",
		Org:      repo.org,
		Repo:     repo.name,
		Path:     repo.workPath,
	}
}

func fakeClientForRepos(repos ...testRepo) fakeClient {
	grouped := make(map[string]map[string]remote.Repository)
	for _, repo := range repos {
		if grouped[repo.org] == nil {
			grouped[repo.org] = make(map[string]remote.Repository)
		}
		grouped[repo.org][repo.name] = remoteRepo(repo)
	}
	return fakeClient{repos: grouped}
}

func remoteRepo(repo testRepo) remote.Repository {
	return remote.Repository{
		Name:          repo.name,
		FullName:      repo.org + "/" + repo.name,
		CloneURL:      repo.remotePath,
		DefaultBranch: repo.defaultBranch,
		Empty:         repo.empty,
		Archived:      repo.archived,
	}
}

func createEmptyTestRepo(t *testing.T, baseDir, org, name, defaultBranch, workPath string) testRepo {
	t.Helper()
	remotePath := filepath.Join(baseDir, name+"-remote.git")
	runGit(t, baseDir, "init", "--bare", remotePath)
	runGit(t, remotePath, "symbolic-ref", "HEAD", "refs/heads/"+defaultBranch)
	workPath = cloneRepo(t, remotePath, workPath)
	return testRepo{
		org:           org,
		name:          name,
		defaultBranch: defaultBranch,
		remotePath:    remotePath,
		workPath:      workPath,
		empty:         true,
	}
}

func createTestRepo(t *testing.T, baseDir, org, name, defaultBranch, workPath string) testRepo {
	t.Helper()

	sourcePath := filepath.Join(baseDir, name+"-source")
	remotePath := filepath.Join(baseDir, name+"-remote.git")

	runGit(t, baseDir, "init", sourcePath)
	configureGitIdentity(t, sourcePath)
	runGit(t, sourcePath, "switch", "-c", defaultBranch)
	commitFile(t, sourcePath, "README.md", name+"\n", "initial commit")

	runGit(t, baseDir, "init", "--bare", remotePath)
	runGit(t, sourcePath, "remote", "add", "origin", remotePath)
	runGit(t, sourcePath, "push", "-u", "origin", defaultBranch)
	runGit(t, remotePath, "symbolic-ref", "HEAD", "refs/heads/"+defaultBranch)

	workPath = cloneRepo(t, remotePath, workPath)
	return testRepo{
		org:           org,
		name:          name,
		defaultBranch: defaultBranch,
		remotePath:    remotePath,
		workPath:      workPath,
	}
}

func cloneRepo(t *testing.T, remotePath, workPath string) string {
	t.Helper()
	runGit(t, filepath.Dir(workPath), "clone", remotePath, workPath)
	configureGitIdentity(t, workPath)
	return workPath
}

func configureGitIdentity(t *testing.T, dir string) {
	t.Helper()
	runGit(t, dir, "config", "user.name", "Test User")
	runGit(t, dir, "config", "user.email", "test@example.com")
}

func commitFile(t *testing.T, dir, relativePath, contents, message string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, relativePath), contents)
	runGit(t, dir, "add", relativePath)
	runGit(t, dir, "commit", "-m", message)
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", path, err)
	}
	if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
}

func currentBranch(t *testing.T, dir string) string {
	t.Helper()
	return strings.TrimSpace(runGit(t, dir, "branch", "--show-current"))
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	originalStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe(): %v", err)
	}
	os.Stdout = writer
	defer func() {
		os.Stdout = originalStdout
	}()

	var output []byte
	var readErr error
	drained := make(chan struct{})
	go func() {
		output, readErr = io.ReadAll(reader)
		close(drained)
	}()
	defer reader.Close()
	defer writer.Close()

	fn()

	if err := writer.Close(); err != nil {
		t.Fatalf("closing writer: %v", err)
	}
	<-drained
	if readErr != nil {
		t.Fatalf("reading captured stdout: %v", readErr)
	}
	return string(output)
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s (dir=%s) failed: %v\n%s", strings.Join(args, " "), dir, err, string(output))
	}
	return string(output)
}
