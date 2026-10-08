//go:build linux

package repo

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitea.swiftstrike.ai/swiftstrike/tugboat/internal/config"
	"gitea.swiftstrike.ai/swiftstrike/tugboat/internal/remote"
)

type renameFixture struct {
	m       *Manager
	target  config.Target
	client  fakeClient
	old     remote.Repository
	newRepo remote.Repository
	source  string
	archive string
}

type archiveListingClient struct {
	remote.Client
	archives []remote.Repository
	err      error
}

func (c archiveListingClient) ListArchivedRepos() ([]remote.Repository, error) {
	return c.archives, c.err
}

func transferredFixture(t *testing.T, saved bool, sameName bool) renameFixture {
	f := renamedFixture(t, saved)
	delete(f.client.repos["t1"], f.old.Name)
	f.old.FullName = "t1-archive/" + f.old.Name
	if sameName {
		f.old.Name = "perception"
		f.old.FullName = "t1-archive/perception"
	}
	f.client.repos["t1-archive"] = map[string]remote.Repository{f.old.Name: f.old}
	f.m.providers["fake"] = archiveListingClient{Client: f.client, archives: []remote.Repository{f.old}}
	return f
}

func TestCloneOnlyRetainsTransferredCheckouts(t *testing.T) {
	for _, saved := range []bool{false, true} {
		f := transferredFixture(t, saved, true)
		before := gitText(t, f.source, "show-ref")
		configBefore, _ := os.ReadFile(filepath.Join(f.source, ".git", "config"))
		var syncErr error
		output := captureStdout(t, func() { syncErr = f.m.Sync(nil, SyncOptions{Mode: SyncCloneOnly, Workers: 1}) })
		configAfter, _ := os.ReadFile(filepath.Join(f.source, ".git", "config"))
		if syncErr == nil || !strings.Contains(syncErr.Error(), "transferred to t1-archive/perception") || gitText(t, f.source, "show-ref") != before || string(configAfter) != string(configBefore) {
			t.Fatalf("transfer was changed or not reported: %v; %s", syncErr, output)
		}
	}
}

func TestTransferredArchiveCleanupAndReplacement(t *testing.T) {
	for _, sameName := range []bool{false, true} {
		f := transferredFixture(t, false, sameName)
		output := captureStdout(t, func() {
			if err := f.m.Sync(nil, SyncOptions{Workers: 1}); err != nil {
				t.Fatal(err)
			}
		})
		id, err := readIdentity(f.source)
		if err != nil || id == nil || id.RepositoryID != f.newRepo.ID || isGitRepo(f.archive) || !strings.Contains(output, "removed transferred archive") || !strings.Contains(output, "replacement clone ready") {
			t.Fatalf("transferred archive was not replaced: %+v, %v; %s", id, err, output)
		}
		assertProgressCounts(t, output, 1)
	}
}

func TestTransferredArchiveBehindRemoteCanBeSafelyCleaned(t *testing.T) {
	f := transferredFixture(t, false, true)
	seed := cloneRepo(t, f.old.CloneURL, filepath.Join(t.TempDir(), "seed"))
	commitFile(t, seed, "later.txt", "later archive work", "later commit")
	runGit(t, seed, "push", "origin", "main")
	output := captureStdout(t, func() {
		if err := f.m.Sync(nil, SyncOptions{Workers: 1}); err != nil {
			t.Fatal(err)
		}
	})
	id, err := readIdentity(f.source)
	if err != nil || id == nil || id.RepositoryID != f.newRepo.ID {
		t.Fatalf("behind archive not replaced: %+v,%v; %s", id, err, output)
	}
	assertProgressCounts(t, output, 1)
}

func TestTransferredArchiveWithoutReplacementIsRemovedOnce(t *testing.T) {
	f := transferredFixture(t, true, true)
	delete(f.client.repos["t1"], f.newRepo.Name)
	output := captureStdout(t, func() {
		if err := f.m.Sync(nil, SyncOptions{Workers: 1}); err != nil {
			t.Fatal(err)
		}
	})
	if isGitRepo(f.source) || !strings.Contains(output, "[REMOVE]") {
		t.Fatalf("archive retained: %s", output)
	}
	assertProgressCounts(t, output, 1)
}

func TestTransferredCleanupRetainsLocalWorkAndFailedClones(t *testing.T) {
	for _, kind := range []string{"stash", "dirty", "local commit", "clone failure"} {
		t.Run(kind, func(t *testing.T) {
			f := transferredFixture(t, false, true)
			switch kind {
			case "stash":
				writeFile(t, filepath.Join(f.source, "stash.txt"), "work")
				runGit(t, f.source, "stash", "push", "-u")
			case "dirty":
				writeFile(t, filepath.Join(f.source, "dirty.txt"), "work")
			case "local commit":
				commitFile(t, f.source, "local.txt", "work", "local work")
			case "clone failure":
				_ = os.RemoveAll(f.newRepo.CloneURL)
			}
			refs := gitText(t, f.source, "show-ref")
			configBefore, _ := os.ReadFile(filepath.Join(f.source, ".git", "config"))
			var syncErr error
			output := captureStdout(t, func() { syncErr = f.m.Sync(nil, SyncOptions{Workers: 1}) })
			configAfter, _ := os.ReadFile(filepath.Join(f.source, ".git", "config"))
			if (syncErr != nil) != (kind == "clone failure") || !isGitRepo(f.source) || gitText(t, f.source, "show-ref") != refs || string(configBefore) != string(configAfter) {
				t.Fatalf("unsafe transfer cleanup changed checkout: %v; %s", syncErr, output)
			}
			assertProgressCounts(t, output, 1)
		})
	}
}

func renamedFixture(t *testing.T, saved bool) renameFixture {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "work")
	remoteRoot := filepath.Join(base, "remotes")
	for _, dir := range []string{root, remoteRoot} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	source := filepath.Join(root, "perception")
	old := createTestRepo(t, base, "t1", "old", "main", source)
	url := filepath.Join(remoteRoot, "perception.git")
	if err := os.Rename(old.remotePath, url); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "remote", "set-url", "origin", url)
	original := remoteRepo(old)
	original.ID, original.Name, original.FullName, original.CloneURL = 101, "perception", "t1/perception", url
	target := config.Target{Name: "t1", Provider: "fake", Org: "t1", Path: root}
	manager := newTestManager([]config.Target{target}, fakeClient{})
	if saved {
		if err := writeIdentity(source, newIdentity(manager.config.Providers["fake"], "t1", original, url)); err != nil {
			t.Fatal(err)
		}
	}
	archiveURL := filepath.Join(remoteRoot, "perception-yolo.git")
	if err := os.Rename(url, archiveURL); err != nil {
		t.Fatal(err)
	}
	archive := original
	archive.Name, archive.FullName, archive.CloneURL, archive.Archived = "perception-yolo", "t1/perception-yolo", archiveURL, true
	newRepo := createTestRepo(t, base, "t1", "replacement", "main", filepath.Join(base, "replacement-work"))
	if err := os.Rename(newRepo.remotePath, url); err != nil {
		t.Fatal(err)
	}
	replacement := remoteRepo(newRepo)
	replacement.ID, replacement.Name, replacement.FullName, replacement.CloneURL = 202, "perception", "t1/perception", url
	client := fakeClient{repos: map[string]map[string]remote.Repository{"t1": {archive.Name: archive, replacement.Name: replacement}}}
	manager.providers["fake"] = client
	return renameFixture{manager, target, client, archive, replacement, source, filepath.Join(root, archive.Name)}
}

func TestReconcileArchivedRenamePreservesLocalWork(t *testing.T) {
	for _, command := range []string{"sync", "clone"} {
		for _, saved := range []bool{false, true} {
			name := command + "/legacy"
			if saved {
				name = command + "/saved"
			}
			t.Run(name, func(t *testing.T) {
				f := renamedFixture(t, saved)
				runGit(t, f.source, "switch", "-c", "feature/local")
				commitFile(t, f.source, ".gitignore", "ignored.txt\n", "local work")
				writeFile(t, filepath.Join(f.source, "stash.txt"), "stashed")
				runGit(t, f.source, "stash", "push", "-u")
				writeFile(t, filepath.Join(f.source, "README.md"), "dirty tracked work")
				writeFile(t, filepath.Join(f.source, "untracked.txt"), "untracked")
				writeFile(t, filepath.Join(f.source, "ignored.txt"), "ignored")
				head := gitText(t, f.source, "rev-parse", "HEAD")
				refs := gitText(t, f.source, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads", "refs/tags", "refs/stash")
				status := gitText(t, f.source, "status", "--porcelain", "--untracked-files=all")
				for run := 0; run < 2; run++ {
					captureStdout(t, func() {
						var err error
						if command == "sync" {
							err = f.m.Sync(nil, SyncOptions{Workers: 2})
						} else {
							err = f.m.Clone(nil, false, false, 2)
						}
						if err != nil {
							t.Fatal(err)
						}
					})
				}
				if gitText(t, f.archive, "rev-parse", "HEAD") != head || gitText(t, f.archive, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads", "refs/tags", "refs/stash") != refs || gitText(t, f.archive, "status", "--porcelain", "--untracked-files=all") != status {
					t.Fatal("local Git state was changed")
				}
				for filename, want := range map[string]string{"README.md": "dirty tracked work", "untracked.txt": "untracked", "ignored.txt": "ignored"} {
					data, err := os.ReadFile(filepath.Join(f.archive, filename))
					if err != nil || string(data) != want {
						t.Fatalf("lost %s: %q, %v", filename, data, err)
					}
				}
				if got := strings.TrimSpace(gitText(t, f.archive, "remote", "get-url", "origin")); got != f.old.CloneURL {
					t.Fatalf("archive origin = %s", got)
				}
				if got := strings.TrimSpace(gitText(t, f.source, "remote", "get-url", "origin")); got != f.newRepo.CloneURL {
					t.Fatalf("replacement origin = %s", got)
				}
				for dir, want := range map[string]int64{f.archive: 101, f.source: 202} {
					id, err := readIdentity(dir)
					if err != nil || id == nil || id.RepositoryID != want || id.Pending != nil {
						t.Fatalf("identity for %s: %+v, %v", dir, id, err)
					}
				}
			})
		}
	}
}

func TestLegacyRecoveryAfterFetchingWrongRepository(t *testing.T) {
	f := renamedFixture(t, false)
	runGit(t, f.source, "fetch", "origin")
	oldHead := gitText(t, f.source, "rev-parse", "HEAD")
	captureStdout(t, func() {
		if err := f.m.Sync(nil, SyncOptions{Workers: 1}); err != nil {
			t.Fatal(err)
		}
	})
	if gitText(t, f.archive, "rev-parse", "HEAD") != oldHead {
		t.Fatal("local history was rebased onto replacement")
	}
}

func TestIdentityChecksDoNotFetchIntoMismatchedCheckout(t *testing.T) {
	f := renamedFixture(t, true)
	refs := gitText(t, f.source, "show-ref")
	identity, _ := os.ReadFile(identityPath(f.source))
	for _, command := range []string{"status", "list", "push"} {
		output := captureStdout(t, func() {
			var err error
			switch command {
			case "status":
				err = f.m.Status(nil, StatusOptions{Workers: 1})
			case "list":
				err = f.m.List(nil, false, 1)
			case "push":
				err = f.m.Push(nil, 1)
			}
			if err != nil {
				t.Fatal(err)
			}
		})
		if !strings.Contains(output, "renamed to t1/perception-yolo") {
			t.Fatalf("%s did not explain rename: %s", command, output)
		}
		if command == "list" && (!strings.Contains(output, "[ ] perception ") || strings.Contains(output, "[x] perception (")) {
			t.Fatalf("replacement incorrectly shown as cloned: %s", output)
		}
		if gitText(t, f.source, "show-ref") != refs {
			t.Fatalf("%s changed refs", command)
		}
		data, _ := os.ReadFile(identityPath(f.source))
		if string(data) != string(identity) {
			t.Fatalf("%s changed identity", command)
		}
	}
}

func TestAmbiguousLegacyHistoriesAreRetained(t *testing.T) {
	f := renamedFixture(t, false)
	duplicate := f.old
	duplicate.ID, duplicate.Name, duplicate.FullName = 303, "another-archive", "t1/another-archive"
	f.client.repos["t1"][duplicate.Name] = duplicate
	refs := gitText(t, f.source, "show-ref")
	output := captureStdout(t, func() {
		if err := f.m.Sync(nil, SyncOptions{RemoveArchived: true, Workers: 1}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(output, "identity is ambiguous") || !strings.Contains(output, "ID 101") || !strings.Contains(output, "ID 303") {
		t.Fatalf("missing candidate diagnostic: %s", output)
	}
	if !isGitRepo(f.source) || isGitRepo(f.archive) || gitText(t, f.source, "show-ref") != refs {
		t.Fatal("ambiguous checkout was changed")
	}
	if _, err := os.Stat(identityPath(f.source)); !os.IsNotExist(err) {
		t.Fatal("ambiguous identity was persisted")
	}
}

func TestMatchingNamedUpstreamAvoidsArchiveProbes(t *testing.T) {
	f := renamedFixture(t, false)
	// A current upstream with related history takes the ordinary verification
	// path, even when an archive has copied history or is unavailable.
	runGit(t, f.newRepo.CloneURL, "fetch", f.old.CloneURL, "+refs/heads/main:refs/heads/main")
	archive := f.old
	archive.CloneURL = filepath.Join(t.TempDir(), "unavailable.git")
	f.client.repos["t1"][archive.Name] = archive
	f.m.providers["fake"] = archiveListingClient{Client: f.client, err: errors.New("archive discovery must not run on the fast path")}
	head := gitText(t, f.source, "rev-parse", "HEAD")
	output := captureStdout(t, func() {
		if err := f.m.Sync(nil, SyncOptions{Workers: 1}); err != nil {
			t.Fatal(err)
		}
	})
	id, err := readIdentity(f.source)
	if err != nil || id == nil || id.RepositoryID != f.newRepo.ID || isGitRepo(f.archive) || gitText(t, f.source, "rev-parse", "HEAD") != head {
		t.Fatalf("ordinary checkout was not adopted safely: %+v, %v; %s", id, err, output)
	}
	assertProgressCounts(t, output, 1)
}

func TestHistoryProbeTransfersOnlyAncestry(t *testing.T) {
	base := t.TempDir()
	r := createTestRepo(t, base, "acme", "app", "main", filepath.Join(base, "app"))
	runGit(t, r.remotePath, "config", "uploadpack.allowFilter", "true")
	probe := &historyProbe{}
	defer probe.close()
	match, err := probe.matches(r.workPath, "fake/acme", "", "https", remoteRepo(r))
	if err != nil || !match {
		t.Fatalf("matching ancestry: %v, %v", match, err)
	}
	types := gitText(t, probe.dir, "cat-file", "--batch-all-objects", "--batch-check=%(objecttype)")
	if strings.Contains(types, "blob") || strings.Contains(types, "tree") {
		t.Fatalf("identity probe downloaded file contents: %s", types)
	}
}

func TestLegacyOriginRedirectIsVerifiedBeforeRepair(t *testing.T) {
	for _, sameID := range []bool{true, false} {
		t.Run(fmt.Sprint(sameID), func(t *testing.T) {
			f := renamedFixture(t, false)
			// The directory already has the new name, but its origin retains an
			// old alias. Only a provider-confirmed redirect may repair that URL.
			runGit(t, f.newRepo.CloneURL, "fetch", f.old.CloneURL, "+refs/heads/main:refs/heads/main")
			oldURL := filepath.Join(filepath.Dir(f.newRepo.CloneURL), "old-alias.git")
			runGit(t, f.source, "remote", "set-url", "origin", oldURL)
			alias := f.newRepo
			if !sameID {
				alias.ID++
			}
			f.client.repos["t1"]["old-alias"] = alias
			output := captureStdout(t, func() { _ = f.m.Sync(nil, SyncOptions{Workers: 1}) })
			origin := strings.TrimSpace(gitText(t, f.source, "remote", "get-url", "origin"))
			if sameID {
				if origin != f.newRepo.CloneURL || !strings.Contains(output, "origin updated") {
					t.Fatalf("verified alias was not repaired: %s; %s", origin, output)
				}
			} else if origin != oldURL || !strings.Contains(output, "[ERROR]") {
				t.Fatalf("unverified alias was changed: %s; %s", origin, output)
			}
			assertProgressCounts(t, output, 1)
		})
	}
}

func TestReconcileBlockedMovesPreserveCheckout(t *testing.T) {
	for _, reason := range []string{"occupied", "linked", "nested", "operation", "pushurl"} {
		t.Run(reason, func(t *testing.T) {
			f := renamedFixture(t, true)
			switch reason {
			case "occupied":
				if err := os.Mkdir(f.archive, 0755); err != nil {
					t.Fatal(err)
				}
				writeFile(t, filepath.Join(f.archive, "keep"), "keep")
			case "linked":
				runGit(t, f.source, "worktree", "add", "-b", "linked", filepath.Join(t.TempDir(), "linked"))
			case "nested":
				nested := filepath.Join(f.source, "child")
				if err := os.Mkdir(nested, 0755); err != nil {
					t.Fatal(err)
				}
				runGit(t, nested, "init")
			case "operation":
				writeFile(t, filepath.Join(f.source, ".git", "MERGE_HEAD"), strings.TrimSpace(gitText(t, f.source, "rev-parse", "HEAD")))
			case "pushurl":
				runGit(t, f.source, "remote", "set-url", "--push", "origin", filepath.Join(t.TempDir(), "different.git"))
			}
			configBefore, _ := os.ReadFile(filepath.Join(f.source, ".git", "config"))
			captureStdout(t, func() { _ = f.m.Sync(nil, SyncOptions{Workers: 1}) })
			if !isGitRepo(f.source) {
				t.Fatal("blocked checkout was moved")
			}
			configAfter, _ := os.ReadFile(filepath.Join(f.source, ".git", "config"))
			if string(configBefore) != string(configAfter) {
				t.Fatal("blocked origin was rewritten")
			}
		})
	}
}

func TestReplacementCloneFailureLeavesOldCheckoutUnchanged(t *testing.T) {
	f := renamedFixture(t, true)
	replacement := f.newRepo
	replacement.CloneURL = filepath.Join(t.TempDir(), "missing.git")
	f.client.repos["t1"][replacement.Name] = replacement
	configBefore, _ := os.ReadFile(filepath.Join(f.source, ".git", "config"))
	identityBefore, _ := os.ReadFile(identityPath(f.source))
	var runErr error
	captureStdout(t, func() { runErr = f.m.Sync(nil, SyncOptions{Workers: 1}) })
	if runErr == nil {
		t.Fatal("replacement clone failure was not returned")
	}
	if !isGitRepo(f.source) || isGitRepo(f.archive) {
		t.Fatal("original checkout was moved on clone failure")
	}
	configAfter, _ := os.ReadFile(filepath.Join(f.source, ".git", "config"))
	identityAfter, _ := os.ReadFile(identityPath(f.source))
	if string(configBefore) != string(configAfter) || string(identityBefore) != string(identityAfter) {
		t.Fatal("original metadata changed on clone failure")
	}
}

func TestPendingReplacementResumesAfterInterruption(t *testing.T) {
	for _, published := range []bool{false, true} {
		name := "not published"
		if published {
			name = "published"
		}
		t.Run(name, func(t *testing.T) {
			f := renamedFixture(t, true)
			runGit(t, f.source, "remote", "set-url", "origin", f.old.CloneURL)
			id := newIdentity(f.m.config.Providers["fake"], "t1", f.old, f.old.CloneURL)
			id.Pending = &pendingReplacement{Name: f.newRepo.Name, RepositoryID: f.newRepo.ID}
			if err := writeIdentity(f.source, id); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(f.source, f.archive); err != nil {
				t.Fatal(err)
			}
			if published {
				if err := f.m.cloneVerified("fake", "t1", f.newRepo, f.source); err != nil {
					t.Fatal(err)
				}
			}
			captureStdout(t, func() {
				if err := f.m.Sync(nil, SyncOptions{Workers: 1}); err != nil {
					t.Fatal(err)
				}
			})
			if !isGitRepo(f.source) || !isGitRepo(f.archive) {
				t.Fatal("recovery did not keep both checkouts")
			}
			old, err := readIdentity(f.archive)
			if err != nil || old.Pending != nil {
				t.Fatalf("pending marker not cleared: %+v %v", old, err)
			}
		})
	}
}

func TestExplicitTargetsAndFoldoutsAreNotRelocated(t *testing.T) {
	for _, foldout := range []bool{false, true} {
		name := "explicit"
		if foldout {
			name = "foldout"
		}
		t.Run(name, func(t *testing.T) {
			f := renamedFixture(t, true)
			target := f.target
			target.Repo, target.Path = "perception", f.source
			if foldout {
				base := t.TempDir()
				parent := createTestRepo(t, base, "t1", "parent", "main", filepath.Join(base, "parent-work"))
				child := filepath.Join(parent.workPath, "child")
				if err := os.Rename(f.source, child); err != nil {
					t.Fatal(err)
				}
				f.source = child
				commitFile(t, parent.workPath, ".gitignore", "child/\n", "ignore child")
				commitFile(t, parent.workPath, ".tugboat.json", `{"repos":[{"name":"t1/perception","target":"child"}]}`, "foldout")
				runGit(t, parent.workPath, "push", "origin", "main")
				f.client.repos["t1"][parent.name] = remoteRepo(parent)
				target = repoTarget(parent)
			}
			f.m.config.Targets = []config.Target{target}
			output := captureStdout(t, func() {
				if err := f.m.Sync(nil, SyncOptions{Workers: 1}); err != nil {
					t.Fatal(err)
				}
				if err := f.m.Clone(nil, false, false, 1); err != nil {
					t.Fatal(err)
				}
			})
			if !strings.Contains(output, "renamed to t1/perception-yolo") || !isGitRepo(f.source) || isGitRepo(f.archive) {
				t.Fatalf("explicit path was not retained: %s", output)
			}
		})
	}
}

func TestReplacementExclusionsAndArchiveCleanup(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		name := "excluded"
		if cleanup {
			name = "cleanup"
		}
		t.Run(name, func(t *testing.T) {
			f := renamedFixture(t, true)
			if !cleanup {
				f.m.config.Targets[0].Exclude = []string{"perception"}
			}
			captureStdout(t, func() {
				if err := f.m.Sync(nil, SyncOptions{RemoveArchived: cleanup, Workers: 1}); err != nil {
					t.Fatal(err)
				}
			})
			if cleanup {
				if isGitRepo(f.archive) || !isGitRepo(f.source) {
					t.Fatal("cleanup did not remove verified archive and keep replacement")
				}
			} else {
				if !isGitRepo(f.archive) || isGitRepo(f.source) {
					t.Fatal("excluded replacement was cloned")
				}
			}
		})
	}
}

type changingIdentityClient struct {
	fakeClient
	calls int
}

func (c *changingIdentityClient) GetRepo(org, name string) (*remote.Repository, error) {
	r, err := c.fakeClient.GetRepo(org, name)
	if name == "perception" {
		c.calls++
		if c.calls >= 2 && r != nil {
			r.ID++
		}
	}
	return r, err
}

func TestProviderIdentityChangeDuringCloneIsRejected(t *testing.T) {
	f := renamedFixture(t, true)
	f.m.providers["fake"] = &changingIdentityClient{fakeClient: f.client}
	var err error
	captureStdout(t, func() { err = f.m.Sync(nil, SyncOptions{Workers: 1}) })
	if err == nil || !isGitRepo(f.source) || isGitRepo(f.archive) {
		t.Fatalf("provider ID change was not rejected: %v", err)
	}
}

func TestIdentityScopeAndTokenFreeStorage(t *testing.T) {
	f := renamedFixture(t, true)
	p := f.m.config.Providers["fake"]
	p.APIURL = "https://user:secret@example.invalid/api/?token=secret#secret"
	id := newIdentity(p, "t1", f.old, "https://user:secret@example.invalid/t1/perception-yolo.git?token=secret")
	if err := writeIdentity(f.source, id); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(identityPath(f.source))
	if strings.Contains(string(data), "secret") {
		t.Fatal("identity contains credentials")
	}
	f.m.config.Providers["fake"] = p
	probe := &historyProbe{}
	defer probe.close()
	s := f.m.inspectIdentity(statusJob{path: f.source, provider: "fake", org: "t1", name: "perception"}, f.client.repos["t1"], probe)
	if s.Error == "" {
		t.Fatal("custom origin was not rejected")
	}
	p.APIURL = "https://another.invalid"
	f.m.config.Providers["fake"] = p
	s = f.m.inspectIdentity(statusJob{path: f.source, provider: "fake", org: "t1", name: "perception"}, f.client.repos["t1"], probe)
	if !strings.Contains(s.IdentityIssue, "another provider instance") {
		t.Fatal("provider scope mismatch not detected")
	}
}

func TestNoReplaceMoveDoesNotOverwriteEvenEmptyDirectory(t *testing.T) {
	base := t.TempDir()
	from, to := filepath.Join(base, "from"), filepath.Join(base, "to")
	for _, dir := range []string{from, to} {
		if err := os.Mkdir(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(from, "keep"), "keep")
	if err := renameNoReplace(from, to); err == nil {
		t.Fatal("occupied destination was overwritten")
	}
	if _, err := os.Stat(filepath.Join(from, "keep")); err != nil {
		t.Fatal(err)
	}
}

func TestArchivedRemovalRejectsChangedRepositoryID(t *testing.T) {
	f := renamedFixture(t, true)
	runGit(t, f.source, "remote", "set-url", "origin", f.old.CloneURL)
	id := newIdentity(f.m.config.Providers["fake"], "t1", f.old, f.old.CloneURL)
	if err := writeIdentity(f.source, id); err != nil {
		t.Fatal(err)
	}
	s := RepoStatus{Path: f.source, Org: "t1", Name: "perception", RemoteName: f.old.Name, Provider: "fake", RepositoryID: f.old.ID, identity: id}
	r := f.old
	r.ID++
	f.client.repos["t1"][r.Name] = r
	if _, err := f.m.confirmArchivedRepository(s); err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("identity change not rejected: %v", err)
	}
}

func TestMetadataFailureDoesNotFetch(t *testing.T) {
	f := renamedFixture(t, true)
	f.client.listErr = map[string]error{"t1": errors.New("unavailable")}
	f.m.providers["fake"] = f.client
	refs := gitText(t, f.source, "show-ref")
	captureStdout(t, func() { _ = f.m.Sync(nil, SyncOptions{Workers: 1}) })
	if gitText(t, f.source, "show-ref") != refs || !isGitRepo(f.source) {
		t.Fatal("metadata failure changed checkout")
	}
}

func TestLegacyShallowAndEmptyHistoriesRequireIdentification(t *testing.T) {
	for _, kind := range []string{"shallow", "empty"} {
		t.Run(kind, func(t *testing.T) {
			f := renamedFixture(t, false)
			switch kind {
			case "shallow":
				runGit(t, f.source, "fetch", "--depth=1", "origin")
			case "empty":
				backup := filepath.Join(t.TempDir(), "backup")
				if err := os.Rename(f.source, backup); err != nil {
					t.Fatal(err)
				}
				base := t.TempDir()
				empty := createEmptyTestRepo(t, base, "t1", "empty", "main", f.source)
				runGit(t, empty.workPath, "remote", "set-url", "origin", f.newRepo.CloneURL)
			}
			output := captureStdout(t, func() {
				if err := f.m.Sync(nil, SyncOptions{Workers: 1}); err != nil {
					t.Fatal(err)
				}
			})
			if !strings.Contains(output, "identity is ambiguous") || !isGitRepo(f.source) || isGitRepo(f.archive) {
				t.Fatalf("ambiguous %s checkout changed: %s", kind, output)
			}
		})
	}
}

func TestPureRenameAndProviderAliasChange(t *testing.T) {
	f := renamedFixture(t, true)
	r := f.old
	r.Archived = false
	f.client.repos["t1"][r.Name] = r
	delete(f.client.repos["t1"], f.newRepo.Name)
	// Sync provisions missing repositories after preserving the renamed checkout.
	unrelated := f.newRepo
	unrelated.Name, unrelated.FullName = "unrelated", "t1/unrelated"
	f.client.repos["t1"][unrelated.Name] = unrelated
	f.m.config.Providers["renamed-provider"] = f.m.config.Providers["fake"]
	f.m.config.Targets[0].Provider = "renamed-provider"
	f.m.providers["renamed-provider"] = f.client
	captureStdout(t, func() {
		if err := f.m.Sync(nil, SyncOptions{Workers: 1}); err != nil {
			t.Fatal(err)
		}
	})
	if !isGitRepo(f.archive) || isGitRepo(f.source) || !isGitRepo(filepath.Join(f.target.Path, "unrelated")) {
		t.Fatal("pure rename did not preserve the original and clone the missing repository")
	}
}

type hookRepositoryClient struct {
	remote.Client
	hook func(string, string)
}

func (c hookRepositoryClient) GetRepo(org, name string) (*remote.Repository, error) {
	c.hook(org, name)
	return c.Client.GetRepo(org, name)
}

func TestFailedPublicationRetainsArchiveAndResumes(t *testing.T) {
	f := renamedFixture(t, true)
	newChecks := 0
	f.m.providers["fake"] = hookRepositoryClient{Client: f.client, hook: func(org, name string) {
		if name != f.newRepo.Name {
			return
		}
		newChecks++
		if newChecks == 3 {
			if err := os.Mkdir(f.source, 0755); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(f.source, "keep"), "external content")
		}
	}}
	var runErr error
	output := captureStdout(t, func() { runErr = f.m.Sync(nil, SyncOptions{RemoveArchived: true, Workers: 1}) })
	if runErr == nil || !strings.Contains(output, "rollback retained checkout") {
		t.Fatalf("publication failure not returned: %v %s", runErr, output)
	}
	if !isGitRepo(f.archive) {
		t.Fatal("preserved archive was lost or cleaned up")
	}
	data, err := os.ReadFile(filepath.Join(f.source, "keep"))
	if err != nil || string(data) != "external content" {
		t.Fatal("occupied replacement was overwritten")
	}
	id, err := readIdentity(f.archive)
	if err != nil || id.Pending == nil {
		t.Fatal("recovery marker was lost")
	}
	if err := os.Remove(filepath.Join(f.source, "keep")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.source); err != nil {
		t.Fatal(err)
	}
	f.m.providers["fake"] = f.client
	captureStdout(t, func() {
		if err := f.m.Sync(nil, SyncOptions{Workers: 1}); err != nil {
			t.Fatal(err)
		}
	})
	if !isGitRepo(f.archive) || !isGitRepo(f.source) {
		t.Fatal("next sync failed to resume")
	}
}

func TestFailedMoveRestoresExactOriginAndIdentity(t *testing.T) {
	f := renamedFixture(t, true)
	configBefore, _ := os.ReadFile(filepath.Join(f.source, ".git", "config"))
	identityBefore, _ := os.ReadFile(identityPath(f.source))
	checks := 0
	f.m.providers["fake"] = hookRepositoryClient{Client: f.client, hook: func(org, name string) {
		if name != f.newRepo.Name {
			return
		}
		checks++
		if checks == 2 {
			if err := os.Mkdir(f.archive, 0755); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(f.archive, "keep"), "keep")
		}
	}}
	var runErr error
	captureStdout(t, func() { runErr = f.m.Sync(nil, SyncOptions{Workers: 1}) })
	if runErr == nil || !isGitRepo(f.source) {
		t.Fatal("failed move was not retained")
	}
	configAfter, _ := os.ReadFile(filepath.Join(f.source, ".git", "config"))
	identityAfter, _ := os.ReadFile(identityPath(f.source))
	if string(configBefore) != string(configAfter) || string(identityBefore) != string(identityAfter) {
		t.Fatal("failed move changed origin or saved identity")
	}
}

func TestRecoveryReusesVerifiedStagedReplacement(t *testing.T) {
	f := renamedFixture(t, true)
	holder, staged, err := f.m.stageClone("fake", "t1", f.newRepo, f.target.Path)
	if err != nil {
		t.Fatal(err)
	}
	// A local sentinel confirms that recovery publishes this exact stage.
	writeFile(t, filepath.Join(staged, "sentinel"), "staged content")
	id := newIdentity(f.m.config.Providers["fake"], "t1", f.old, f.old.CloneURL)
	id.Pending = &pendingReplacement{Name: f.newRepo.Name, RepositoryID: f.newRepo.ID, Staging: filepath.Base(holder)}
	runGit(t, f.source, "remote", "set-url", "origin", f.old.CloneURL)
	if err := writeIdentity(f.source, id); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(f.source, f.archive); err != nil {
		t.Fatal(err)
	}
	captureStdout(t, func() {
		if err := f.m.Sync(nil, SyncOptions{Workers: 1}); err != nil {
			t.Fatal(err)
		}
	})
	if data, err := os.ReadFile(filepath.Join(f.source, "sentinel")); err != nil || string(data) != "staged content" {
		t.Fatal("verified stage was not resumed")
	}
	if _, err := os.Stat(holder); !os.IsNotExist(err) {
		t.Fatal("published staging directory was not cleaned up")
	}
}

func TestSavedIdentityMissingFromProviderIsRetained(t *testing.T) {
	f := renamedFixture(t, true)
	delete(f.client.repos["t1"], f.old.Name)
	refs := gitText(t, f.source, "show-ref")
	output := captureStdout(t, func() {
		if err := f.m.Sync(nil, SyncOptions{RemoveArchived: true, Workers: 1}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(output, "saved repository ID is missing") || !isGitRepo(f.source) || gitText(t, f.source, "show-ref") != refs {
		t.Fatal("missing saved identity was rebound to the replacement")
	}
}

type staleListingClient struct {
	fakeClient
	stale remote.Repository
}

func (c staleListingClient) ListOrgRepos(org string) ([]remote.Repository, error) {
	return []remote.Repository{c.stale}, nil
}

func TestStaleListingCannotAuthorizeFetchingReplacement(t *testing.T) {
	f := renamedFixture(t, true)
	stale := f.old
	stale.Name, stale.FullName, stale.CloneURL, stale.Archived = "perception", "t1/perception", f.newRepo.CloneURL, false
	f.m.providers["fake"] = staleListingClient{fakeClient: f.client, stale: stale}
	refs := gitText(t, f.source, "show-ref")
	for _, command := range []string{"status", "pull", "push", "sync"} {
		output := captureStdout(t, func() {
			switch command {
			case "status":
				_ = f.m.Status(nil, StatusOptions{Workers: 1})
			case "pull":
				_ = f.m.Pull(nil, 1)
			case "push":
				_ = f.m.Push(nil, 1)
			case "sync":
				_ = f.m.Sync(nil, SyncOptions{Workers: 1})
			}
		})
		if !strings.Contains(output, "provider identity changed") || gitText(t, f.source, "show-ref") != refs {
			t.Fatalf("%s trusted stale metadata: %s", command, output)
		}
	}
}

func TestOriginRewritePreservesTransport(t *testing.T) {
	r := remote.Repository{CloneURL: "https://example.com/t1/new.git", SSHURL: "git@example.com:t1/new.git"}
	for old, want := range map[string]string{"https://example.com/t1/old.git": r.CloneURL, "git@example.com:t1/old.git": r.SSHURL, "ssh://git@example.com/t1/old.git": r.SSHURL} {
		got, err := renamedOrigin(old, r)
		if err != nil || got != want {
			t.Fatalf("origin %s: %s %v", old, got, err)
		}
	}
	if _, err := renamedOrigin("git@example.com:t1/old.git", remote.Repository{CloneURL: r.CloneURL}); err == nil {
		t.Fatal("SSH origin was silently changed to HTTPS")
	}
}

func TestReconcileRejectsCheckoutReplacedAfterInspection(t *testing.T) {
	f := renamedFixture(t, true)
	probe := &historyProbe{}
	defer probe.close()
	s := f.m.inspectIdentity(statusJob{path: f.source, target: "t1", provider: "fake", org: "t1", name: "perception"}, f.client.repos["t1"], probe)
	if err := os.Rename(f.source, filepath.Join(t.TempDir(), "backup")); err != nil {
		t.Fatal(err)
	}
	if err := f.m.cloneVerified("fake", "t1", f.newRepo, f.source); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.reconcileCheckout(f.target, s, f.client.repos["t1"], false, false); err == nil {
		t.Fatal("stale inspection moved a replacement checkout")
	}
	id, err := readIdentity(f.source)
	if err != nil || id == nil || id.RepositoryID != 202 || isGitRepo(f.archive) {
		t.Fatal("replacement identity or path was changed")
	}
}

func TestStaleIdentityAdoptionCannotEraseRecoveryMarker(t *testing.T) {
	f := renamedFixture(t, true)
	runGit(t, f.source, "remote", "set-url", "origin", f.old.CloneURL)
	id := newIdentity(f.m.config.Providers["fake"], "t1", f.old, f.old.CloneURL)
	if err := writeIdentity(f.source, id); err != nil {
		t.Fatal(err)
	}
	probe := &historyProbe{}
	defer probe.close()
	s := f.m.inspectIdentity(statusJob{path: f.source, provider: "fake", org: "t1", name: f.old.Name}, f.client.repos["t1"], probe)
	id.Pending = &pendingReplacement{Name: "perception", RepositoryID: 202}
	if err := writeIdentity(f.source, id); err != nil {
		t.Fatal(err)
	}
	if err := f.m.saveVerifiedIdentity(s); err == nil {
		t.Fatal("stale adoption accepted a changed recovery marker")
	}
	current, err := readIdentity(f.source)
	if err != nil || current.Pending == nil {
		t.Fatal("recovery marker was erased")
	}
}

func TestIdentityVerificationAcceptsFileOriginWithSpaces(t *testing.T) {
	base := filepath.Join(t.TempDir(), "repository paths with spaces")
	if err := os.Mkdir(base, 0755); err != nil {
		t.Fatal(err)
	}
	r := createTestRepo(t, base, "acme", "app", "main", filepath.Join(base, "work"))
	m := newTestManager([]config.Target{repoTarget(r)}, fakeClientForRepos(r))
	output := captureStdout(t, func() {
		if err := m.Status(nil, StatusOptions{ShowAll: true, Workers: 1}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(output, "CLEAN") || strings.Contains(output, "ERROR") {
		t.Fatalf("file origin with spaces was rejected: %s", output)
	}
}

func TestFetchAndPushOriginsUpdateTogether(t *testing.T) {
	f := renamedFixture(t, true)
	runGit(t, f.source, "remote", "set-url", "--push", "origin", f.newRepo.CloneURL)
	captureStdout(t, func() {
		if err := f.m.Sync(nil, SyncOptions{Workers: 1}); err != nil {
			t.Fatal(err)
		}
	})
	if strings.TrimSpace(gitText(t, f.archive, "remote", "get-url", "--push", "origin")) != f.old.CloneURL {
		t.Fatal("explicit push URL was not updated with fetch URL")
	}
}

func TestFailedPushConfigEditDoesNotPartiallyRewriteOrigin(t *testing.T) {
	f := renamedFixture(t, true)
	runGit(t, f.source, "remote", "set-url", "--push", "origin", f.newRepo.CloneURL)
	before := gitText(t, f.source, "config", "--local", "--list")
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	wrapper := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = config ] && [ \"$5\" = remote.origin.pushurl ]; then exit 1; fi\nexec \"$CONFIG_REAL_GIT\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(wrapper, "git"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIG_REAL_GIT", realGit)
	t.Setenv("PATH", wrapper+string(os.PathListSeparator)+os.Getenv("PATH"))
	var runErr error
	captureStdout(t, func() { runErr = f.m.Sync(nil, SyncOptions{Workers: 1}) })
	if runErr == nil || !isGitRepo(f.source) || gitText(t, f.source, "config", "--local", "--list") != before {
		t.Fatal("failed second edit left a partial live config")
	}
}

func TestRecoveryRemovesOnlyOwnedGitConfigLocks(t *testing.T) {
	for _, owned := range []bool{false, true} {
		name := "foreign"
		if owned {
			name = "owned"
		}
		t.Run(name, func(t *testing.T) {
			f := renamedFixture(t, true)
			content := "another Git process's lock"
			if owned {
				content = gitConfigLockMarker
			}
			filename := filepath.Join(f.source, ".git", "config.lock")
			writeFile(t, filename, content)
			captureStdout(t, func() { _ = f.m.Sync(nil, SyncOptions{Workers: 1}) })
			if owned {
				if !isGitRepo(f.archive) || !isGitRepo(f.source) {
					t.Fatal("owned crash lock prevented recovery")
				}
			} else {
				if !isGitRepo(f.source) || isGitRepo(f.archive) {
					t.Fatal("foreign config lock was overwritten")
				}
				if data, err := os.ReadFile(filename); err != nil || string(data) != content {
					t.Fatal("foreign lock was removed")
				}
			}
		})
	}
}

func TestDefaultSyncHandlesPrunableTransferredWorktree(t *testing.T) {
	for _, mode := range []SyncMode{SyncBoth, SyncPull} {
		for _, unpublished := range []bool{false, true} {
			t.Run(fmt.Sprintf("mode=%d/unpublished=%v", mode, unpublished), func(t *testing.T) {
				f := transferredFixture(t, true, false)
				worktree := filepath.Join(t.TempDir(), "stale worktree\nwith newline")
				runGit(t, f.source, "worktree", "add", "-b", "old-feature", worktree)
				if unpublished {
					configureGitIdentity(t, worktree)
					commitFile(t, worktree, "local.txt", "saved work", "unpublished work")
				}
				if err := os.RemoveAll(worktree); err != nil {
					t.Fatal(err)
				}
				before := gitText(t, f.source, "show-ref")
				var runErr error
				output := captureStdout(t, func() { runErr = f.m.Sync(nil, SyncOptions{Mode: mode, Workers: 1}) })
				if unpublished {
					if runErr != nil || !strings.Contains(output, "[SKIP]") || !strings.Contains(output, "branch old-feature (1)") || gitText(t, f.source, "show-ref") != before {
						t.Fatalf("unpublished branch lost: %v; %s", runErr, output)
					}
				} else {
					id, err := readIdentity(f.source)
					if runErr != nil || err != nil || id == nil || id.RepositoryID != f.newRepo.ID {
						t.Fatalf("safe transfer not replaced: %v; %+v; %s", runErr, id, output)
					}
				}
				assertProgressCounts(t, output, 1)
			})
		}
	}
}

func TestTransferredCleanupRetainsLiveLockedAndDetachedWorktrees(t *testing.T) {
	for _, kind := range []string{"live", "locked missing", "detached missing"} {
		t.Run(kind, func(t *testing.T) {
			f := transferredFixture(t, true, false)
			worktree := filepath.Join(t.TempDir(), "linked")
			if kind == "detached missing" {
				runGit(t, f.source, "worktree", "add", "--detach", worktree)
			} else {
				runGit(t, f.source, "worktree", "add", "-b", "feature", worktree)
			}
			if kind == "locked missing" {
				runGit(t, f.source, "worktree", "lock", worktree)
			}
			if kind != "live" {
				if err := os.RemoveAll(worktree); err != nil {
					t.Fatal(err)
				}
			}
			before := gitText(t, f.source, "show-ref")
			var runErr error
			output := captureStdout(t, func() { runErr = f.m.Sync(nil, SyncOptions{Workers: 1}) })
			if runErr != nil || !strings.Contains(output, "[SKIP]") || !strings.Contains(output, "has linked worktrees") || gitText(t, f.source, "show-ref") != before {
				t.Fatalf("protected worktree not retained: %v; %s", runErr, output)
			}
		})
	}
}

func TestBlockedTransferDoesNotBlockUnrelatedMissingClone(t *testing.T) {
	f := transferredFixture(t, true, false)
	writeFile(t, filepath.Join(f.source, "dirty.txt"), "local work")
	unrelated := createTestRepo(t, t.TempDir(), "t1", "unrelated", "main", filepath.Join(t.TempDir(), "seed"))
	f.client.repos["t1"][unrelated.name] = remoteRepo(unrelated)
	var runErr error
	output := captureStdout(t, func() { runErr = f.m.Sync(nil, SyncOptions{Workers: 2}) })
	if runErr != nil || !strings.Contains(output, "[SKIP]") || !strings.Contains(output, "dirty worktree") || !isGitRepo(filepath.Join(f.target.Path, "unrelated")) {
		t.Fatalf("unrelated provisioning blocked: %v; %s", runErr, output)
	}
	assertProgressCounts(t, output, 2)
}

func TestPullOnlyReconcilesReplacementWithoutMixingHistories(t *testing.T) {
	f := renamedFixture(t, true)
	oldHead := gitText(t, f.source, "rev-parse", "HEAD")
	output := captureStdout(t, func() {
		if err := f.m.Sync(nil, SyncOptions{Mode: SyncPull, Workers: 1}); err != nil {
			t.Fatal(err)
		}
	})
	if gitText(t, f.archive, "rev-parse", "HEAD") != oldHead || !isGitRepo(f.source) {
		t.Fatal("replacement reconciliation lost original")
	}
	assertProgressCounts(t, output, 2)
}

func TestDefaultSyncRetiresTransfersButRetainsInOrgArchives(t *testing.T) {
	f := transferredFixture(t, true, false)
	r := createTestRepo(t, t.TempDir(), "t1", "in-org-archive", "main", filepath.Join(f.target.Path, "in-org-archive"))
	r.archived = true
	f.client.repos["t1"][r.name] = remoteRepo(r)
	output := captureStdout(t, func() {
		if err := f.m.Sync(nil, SyncOptions{Workers: 1}); err != nil {
			t.Fatal(err)
		}
	})
	id, err := readIdentity(f.source)
	if err != nil || id == nil || id.RepositoryID != f.newRepo.ID || !isGitRepo(r.workPath) {
		t.Fatalf("wrong default retirement policy: %s", output)
	}
	assertProgressCounts(t, output, 2)
	output = captureStdout(t, func() {
		if err := f.m.Sync(nil, SyncOptions{RemoveArchived: true, Workers: 1}); err != nil {
			t.Fatal(err)
		}
	})
	if _, err := os.Lstat(r.workPath); !os.IsNotExist(err) {
		t.Fatalf("in-org archive not cleaned with flag: %s", output)
	}
	assertProgressCounts(t, output, 2)
}

func TestTransferredCleanupIgnoresStaleRemoteTrackingRefs(t *testing.T) {
	for _, localBranch := range []bool{false, true} {
		t.Run(fmt.Sprint(localBranch), func(t *testing.T) {
			f := transferredFixture(t, true, false)
			runGit(t, f.source, "fetch", "origin") // origin now points at the replacement's unrelated history.
			if localBranch {
				runGit(t, f.source, "branch", "unpublished", "origin/main")
			}
			var runErr error
			output := captureStdout(t, func() { runErr = f.m.Sync(nil, SyncOptions{Workers: 1}) })
			if localBranch {
				if runErr != nil || !strings.Contains(output, "[SKIP]") || !strings.Contains(output, "unpublished commits") {
					t.Fatalf("local branch not protected: %v; %s", runErr, output)
				}
			} else {
				id, err := readIdentity(f.source)
				if runErr != nil || err != nil || id == nil || id.RepositoryID != f.newRepo.ID {
					t.Fatalf("stale fetch cache blocked cleanup: %v; %s", runErr, output)
				}
			}
		})
	}
}

func TestSkippedRenameReservesItsDestination(t *testing.T) {
	f := renamedFixture(t, true)
	r := f.old
	r.Archived = false
	f.client.repos["t1"][r.Name] = r
	delete(f.client.repos["t1"], f.newRepo.Name)
	runGit(t, f.source, "worktree", "add", "-b", "feature", filepath.Join(t.TempDir(), "linked"))
	refs := gitText(t, f.source, "show-ref")
	output := captureStdout(t, func() {
		if err := f.m.Sync(nil, SyncOptions{Workers: 2}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(output, "[SKIP]") || !strings.Contains(output, "has linked worktrees") || isGitRepo(f.archive) || gitText(t, f.source, "show-ref") != refs {
		t.Fatalf("blocked rename destination was provisioned or original changed: %s", output)
	}
	assertProgressCounts(t, output, 1)
}

type gatedArchivesClient struct {
	remote.Client
	archives []remote.Repository
	entered  chan<- struct{}
	release  <-chan struct{}
}

func (c gatedArchivesClient) ListArchivedRepos() ([]remote.Repository, error) {
	select {
	case c.entered <- struct{}{}:
	default:
	}
	<-c.release
	return c.archives, nil
}

func TestCanonicalCheckoutFinishesBeforeArchiveReconciliation(t *testing.T) {
	f := transferredFixture(t, true, false)
	fast := createTestRepo(t, t.TempDir(), "t1", "fast", "main", filepath.Join(f.target.Path, "fast"))
	f.client.repos["t1"][fast.name] = remoteRepo(fast)
	if err := writeIdentity(fast.workPath, newIdentity(f.m.config.Providers["fake"], "t1", remoteRepo(fast), fast.remotePath)); err != nil {
		t.Fatal(err)
	}
	release, entered := make(chan struct{}), make(chan struct{}, 1)
	f.m.providers["fake"] = gatedArchivesClient{Client: f.client, archives: []remote.Repository{f.old}, release: release, entered: entered}
	output := observeCommand(t, func() error { return f.m.Sync(nil, SyncOptions{Workers: 2}) }, func(out *observedOutput) {
		defer close(release)
		out.waitFor(t, "[1/2] [OK]    "+fast.workPath)
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatal("archive reconciliation was not started")
		}
		if strings.Contains(out.String(), "[OK]    "+f.source) || strings.Contains(out.String(), "complete:") {
			t.Fatalf("stalled identity check reported complete: %s", out.String())
		}
	})
	assertProgressCounts(t, output, 2)
}

func TestLegacyActiveRenameMovesWhenCanonicalCheckoutIsMissing(t *testing.T) {
	m, _, _, source, dest := activeRenameFixture(t)
	if err := os.RemoveAll(dest); err != nil {
		t.Fatal(err)
	}
	head := gitText(t, source, "rev-parse", "HEAD")
	output := captureStdout(t, func() {
		if err := m.Sync(nil, SyncOptions{RemoveArchived: true, Workers: 2}); err != nil {
			t.Fatal(err)
		}
	})
	if isGitRepo(source) || !isGitRepo(dest) || gitText(t, dest, "rev-parse", "HEAD") != head || strings.Contains(output, "[REMOVE]") {
		t.Fatalf("sole active checkout not moved safely: %s", output)
	}
	assertProgressCounts(t, output, 1)
}
