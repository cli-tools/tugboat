package repo

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestUnpublishedWorkNamesOnlyAffectedLocalRefs(t *testing.T) {
	base := t.TempDir()
	r := createTestRepo(t, base, "acme", "app", "main", filepath.Join(base, "work"))
	remoteHead := strings.TrimSpace(gitText(t, r.remotePath, "rev-parse", "main"))
	runGit(t, r.workPath, "branch", "already-published")
	runGit(t, r.workPath, "switch", "-c", "unfinished")
	commitFile(t, r.workPath, "local.txt", "work\n", "local work")
	runGit(t, r.workPath, "tag", "saved-work")
	runGit(t, r.workPath, "switch", "--detach", "main")
	commitFile(t, r.workPath, "detached.txt", "detached\n", "detached work")
	other := createTestRepo(t, base, "acme", "other", "main", filepath.Join(base, "other"))
	runGit(t, r.workPath, "fetch", "--no-tags", other.remotePath, "+HEAD:refs/remotes/cached/unrelated")
	work, err := localUnpublishedWork(r.workPath, []string{remoteHead})
	if err != nil || work.count != 2 {
		t.Fatalf("unique unpublished commits: %+v, %v", work, err)
	}
	reason := work.reason()
	for _, want := range []string{"branch unfinished (1)", "tag saved-work (1)", "detached HEAD (1)"} {
		if !strings.Contains(reason, want) {
			t.Fatalf("missing %q: %s", want, reason)
		}
	}
	for _, unwanted := range []string{"already-published", "branch main", "cached/unrelated"} {
		if strings.Contains(reason, unwanted) {
			t.Fatalf("reported unaffected ref %q: %s", unwanted, reason)
		}
	}
}
