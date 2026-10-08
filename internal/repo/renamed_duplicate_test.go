package repo

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitea.swiftstrike.ai/swiftstrike/tugboat/internal/config"
	"gitea.swiftstrike.ai/swiftstrike/tugboat/internal/remote"
)

type renamedAliasClient struct {
	fakeClient
	alias remote.Repository
}

func (c renamedAliasClient) GetRepo(org, name string) (*remote.Repository, error) {
	if name == "mango" {
		r := c.alias
		return &r, nil
	}
	return c.fakeClient.GetRepo(org, name)
}
func (c renamedAliasClient) ListArchivedRepos() ([]remote.Repository, error) {
	return nil, fmt.Errorf("active rename must not scan archives")
}

func activeRenameFixture(t *testing.T) (*Manager, config.Target, remote.Repository, string, string) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "work")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "mango")
	seed := createTestRepo(t, base, "t1", "mango", "main", source)
	canonicalRemote := filepath.Join(base, "perception.git")
	if err := os.Rename(seed.remotePath, canonicalRemote); err != nil {
		t.Fatal(err)
	}
	r := remoteRepo(seed)
	r.Name, r.FullName, r.CloneURL = "perception", "t1/perception", canonicalRemote
	runGit(t, source, "remote", "set-url", "origin", filepath.Join(base, "mango.git"))
	dest := cloneRepo(t, canonicalRemote, filepath.Join(root, "perception"))
	target := config.Target{Name: "t1", Provider: "fake", Org: "t1", Path: root}
	client := renamedAliasClient{fakeClient: fakeClient{repos: map[string]map[string]remote.Repository{"t1": {"perception": r}}}, alias: r}
	manager := newTestManager([]config.Target{target}, client.fakeClient)
	manager.providers["fake"] = client
	if err := writeIdentity(dest, newIdentity(manager.config.Providers["fake"], "t1", r, r.CloneURL)); err != nil {
		t.Fatal(err)
	}
	return manager, target, r, source, dest
}

func TestLegacyActiveRenameAndDuplicateCleanup(t *testing.T) {
	for _, remove := range []bool{false, true} {
		t.Run(fmt.Sprint(remove), func(t *testing.T) {
			m, _, _, source, dest := activeRenameFixture(t)
			// The old clone can be behind; only unpublished work prevents deletion.
			commitFile(t, dest, "later.txt", "published work", "later commit")
			runGit(t, dest, "push", "origin", "main")
			before := gitText(t, dest, "show-ref")
			output := captureStdout(t, func() {
				if err := m.Sync(nil, SyncOptions{RemoveArchived: remove, Workers: 2}); err != nil {
					t.Fatal(err)
				}
			})
			if gitText(t, dest, "show-ref") != before {
				t.Fatal("canonical checkout changed")
			}
			if remove {
				if _, err := os.Lstat(source); !os.IsNotExist(err) || !strings.Contains(output, "[REMOVE]") || !strings.Contains(output, "duplicate of t1/perception") {
					t.Fatalf("duplicate not removed: %s", output)
				}
			} else {
				if !isGitRepo(source) || !strings.Contains(output, "renamed to t1/perception") || !strings.Contains(output, "destination is occupied") || !strings.Contains(output, "use --remove-archived") || strings.Contains(output, "run tugboat sync") {
					t.Fatalf("active rename not identified: %s", output)
				}
			}
			assertProgressCounts(t, output, 2)
		})
	}
}

func TestRenamedDuplicateCleanupPreservesLocalWork(t *testing.T) {
	for _, kind := range []string{"branch", "tag", "dirty", "stash", "worktree", "operation", "nested", "canonical identity", "canonical symlink"} {
		t.Run(kind, func(t *testing.T) {
			m, _, r, source, dest := activeRenameFixture(t)
			reason := ""
			switch kind {
			case "branch":
				runGit(t, source, "switch", "-c", "local-feature")
				commitFile(t, source, "local.txt", "saved work", "unpublished")
				reason = "branch local-feature (1)"
			case "tag":
				commitFile(t, source, "local.txt", "saved work", "unpublished")
				runGit(t, source, "tag", "local-tag")
				runGit(t, source, "reset", "--hard", "HEAD~1")
				reason = "tag local-tag (1)"
			case "dirty":
				writeFile(t, filepath.Join(source, "local.txt"), "work")
				reason = "dirty worktree"
			case "stash":
				writeFile(t, filepath.Join(source, "local.txt"), "work")
				runGit(t, source, "stash", "push", "-u")
				reason = "contains stashes"
			case "worktree":
				runGit(t, source, "worktree", "add", "-b", "feature", filepath.Join(t.TempDir(), "linked"))
				reason = "has linked worktrees"
			case "operation":
				writeFile(t, filepath.Join(source, ".git", "MERGE_HEAD"), strings.TrimSpace(gitText(t, source, "rev-parse", "HEAD"))+"\n")
				reason = "active Git operation"
			case "nested":
				runGit(t, source, "init", "nested")
				reason = "contains nested Git checkout"
			case "canonical identity":
				r.ID++
				if err := writeIdentity(dest, newIdentity(m.config.Providers["fake"], "t1", r, r.CloneURL)); err != nil {
					t.Fatal(err)
				}
				reason = "canonical checkout"
			case "canonical symlink":
				backup := filepath.Join(t.TempDir(), "canonical")
				if err := os.Rename(dest, backup); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(backup, dest); err != nil {
					t.Fatal(err)
				}
				reason = "canonical checkout"
			}
			refs := gitText(t, source, "show-ref")
			before, _ := os.ReadFile(filepath.Join(source, ".git", "config"))
			output := captureStdout(t, func() { _ = m.Sync(nil, SyncOptions{RemoveArchived: true, Workers: 2}) })
			after, _ := os.ReadFile(filepath.Join(source, ".git", "config"))
			if !isGitRepo(source) || gitText(t, source, "show-ref") != refs || string(before) != string(after) || !strings.Contains(output, reason) {
				t.Fatalf("unsafe duplicate cleanup: %s", output)
			}
			if kind != "canonical identity" && !strings.Contains(output, "[SKIP]") {
				t.Fatalf("safety refusal not reported as skip: %s", output)
			}
			total := 2
			if kind == "canonical symlink" {
				total = 1
			}
			assertProgressCounts(t, output, total)
		})
	}
}

func TestLegacyActiveRenameStatusIsReadOnly(t *testing.T) {
	m, _, _, source, _ := activeRenameFixture(t)
	output := captureStdout(t, func() {
		if err := m.Status(nil, StatusOptions{ShowAll: true, Workers: 2}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(output, "renamed to t1/perception") || !isGitRepo(source) {
		t.Fatalf("active rename not reported: %s", output)
	}
	if id, err := readIdentity(source); err != nil || id != nil {
		t.Fatal("read-only status saved identity")
	}
}

func TestActiveRenameRequiresMatchingHistory(t *testing.T) {
	m, target, _, source, _ := activeRenameFixture(t)
	unrelated := createTestRepo(t, t.TempDir(), "t1", "unrelated", "main", filepath.Join(t.TempDir(), "other"))
	runGit(t, source, "fetch", unrelated.remotePath, "+refs/heads/main:refs/heads/other")
	runGit(t, source, "switch", "other")
	refs := gitText(t, source, "show-ref")
	output := captureStdout(t, func() { _ = m.Sync([]string{target.Name}, SyncOptions{RemoveArchived: true, Workers: 2}) })
	if !isGitRepo(source) || gitText(t, source, "show-ref") != refs || strings.Contains(output, "[REMOVE]") {
		t.Fatalf("redirect without shared history authorized removal: %s", output)
	}
}

func TestSavedRenamedDuplicateCanBeCleaned(t *testing.T) {
	m, _, r, source, dest := activeRenameFixture(t)
	origin := strings.TrimSpace(gitText(t, source, "remote", "get-url", "origin"))
	id := newIdentity(m.config.Providers["fake"], "t1", r, origin)
	id.FullName = "t1/mango"
	if err := writeIdentity(source, id); err != nil {
		t.Fatal(err)
	}
	output := captureStdout(t, func() {
		if err := m.Sync(nil, SyncOptions{RemoveArchived: true, Workers: 2}); err != nil {
			t.Fatal(err)
		}
	})
	if isGitRepo(source) || !isGitRepo(dest) || !strings.Contains(output, "[REMOVE]") {
		t.Fatalf("saved duplicate not removed: %s", output)
	}
	assertProgressCounts(t, output, 2)
}

func TestDuplicateCleanupRechecksRedirect(t *testing.T) {
	m, target, r, source, _ := activeRenameFixture(t)
	probe := &historyProbe{}
	defer probe.close()
	repos := map[string]remote.Repository{r.Name: r}
	status := m.inspectIdentity(statusJob{path: source, provider: "fake", org: "t1", name: "mango"}, repos, probe)
	if status.RepositoryID != r.ID {
		t.Fatalf("fixture identity unresolved: %+v", status)
	}
	client := m.providers["fake"].(renamedAliasClient)
	client.alias.ID++
	m.providers["fake"] = client
	err := m.removeRenamedDuplicate(target, status, repos, probe)
	if err == nil || !strings.Contains(err.Error(), "no longer redirects") || !isGitRepo(source) {
		t.Fatalf("changed redirect permitted cleanup: %v", err)
	}
}

func TestDuplicateCleanupKeepsNameReusedByReplacement(t *testing.T) {
	m, _, r, source, dest := activeRenameFixture(t)
	origin := strings.TrimSpace(gitText(t, source, "remote", "get-url", "origin"))
	id := newIdentity(m.config.Providers["fake"], "t1", r, origin)
	id.FullName = "t1/mango"
	if err := writeIdentity(source, id); err != nil {
		t.Fatal(err)
	}
	client := m.providers["fake"].(renamedAliasClient)
	replacement := r
	replacement.ID++
	replacement.Name, replacement.FullName, replacement.CloneURL = "mango", "t1/mango", origin
	client.repos["t1"]["mango"] = replacement
	m.providers["fake"] = client
	output := captureStdout(t, func() {
		if err := m.Sync(nil, SyncOptions{RemoveArchived: true, Workers: 2}); err != nil {
			t.Fatal(err)
		}
	})
	if !isGitRepo(source) || !isGitRepo(dest) || strings.Contains(output, "[REMOVE]") {
		t.Fatalf("replacement path not retained: %s", output)
	}
	assertProgressCounts(t, output, 2)
}
