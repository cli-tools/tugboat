package repo

import (
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
	repos map[string]map[string]remote.Repository
}

func (c fakeClient) ListOrgRepos(orgName string) ([]remote.Repository, error) {
	reposByName := c.repos[orgName]
	repos := make([]remote.Repository, 0, len(reposByName))
	for _, repo := range reposByName {
		repos = append(repos, repo)
	}
	return repos, nil
}

func (c fakeClient) GetRepo(owner, repoName string) (*remote.Repository, error) {
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
		if err := manager.Sync(nil, 1); err != nil {
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
		DefaultBranch: repo.defaultBranch,
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

	fn()

	if err := writer.Close(); err != nil {
		t.Fatalf("closing writer: %v", err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("reading captured stdout: %v", err)
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
