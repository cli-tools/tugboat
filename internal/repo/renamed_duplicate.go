package repo

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gitea.swiftstrike.ai/swiftstrike/tugboat/internal/config"
	"gitea.swiftstrike.ai/swiftstrike/tugboat/internal/remote"
)

// Cleanup can discard an obsolete name only while a separate canonical
// checkout of the same provider repository remains verified and intact.
func (m *Manager) removeRenamedDuplicate(t config.Target, s RepoStatus, repos map[string]remote.Repository, probe *historyProbe) error {
	if s.identity == nil || s.identity.Pending != nil || s.repository == nil || hasStatusError(s) || s.Orphan || s.Transferred || s.ReplacementID != 0 {
		return archiveSkip("renamed repository identity is unresolved")
	}
	r := *s.repository
	dest := filepath.Join(t.Path, r.Name)
	if r.Name == s.Name || repositoryOwner(r, t.Org) != t.Org {
		return archiveSkip("not an obsolete renamed checkout")
	}
	unlock, err := lockCheckout(s.Path)
	if err != nil {
		return archiveSkip("checkout is busy: " + err.Error())
	}
	defer unlock()
	origin, err := checkoutOrigin(s.Path)
	if err != nil {
		return err
	}
	verify := func() error {
		if err := confirmCheckoutDirectory(s); err != nil {
			return err
		}
		if err := checkMoveSafety(s.Path, t); err != nil {
			return err
		}
		stored, err := readIdentity(s.Path)
		if err != nil {
			return err
		}
		if stored != nil && (stored.RepositoryID != s.RepositoryID || stored.ProviderType != s.identity.ProviderType || stored.APIURL != s.identity.APIURL || stored.Pending != nil) {
			return archiveSkip("checkout identity changed during cleanup")
		}
		if stored == nil && !originMatches(origin, r) {
			matches, err := m.originRedirectMatches(statusJob{provider: t.Provider, org: t.Org}, origin, r)
			if err != nil {
				return err
			}
			if !matches {
				return archiveSkip("old upstream no longer redirects to the renamed repository")
			}
		}
		currentOrigin, err := checkoutOrigin(s.Path)
		if err != nil {
			return err
		}
		if currentOrigin != origin {
			return archiveSkip("origin changed during cleanup")
		}
		dirty, err := gitOutput(s.Path, "status", "--porcelain=v1", "--untracked-files=all", "--ignore-submodules=none")
		if err != nil {
			return err
		}
		if strings.TrimSpace(dirty) != "" {
			return archiveSkip("dirty worktree")
		}
		stashes, err := gitOutput(s.Path, "stash", "list")
		if err != nil {
			return err
		}
		if strings.TrimSpace(stashes) != "" {
			return archiveSkip("contains stashes")
		}
		if err := validateArchiveRemovalPath(dest, t); err != nil {
			return archiveSkip("canonical checkout: " + err.Error())
		}
		canonical := m.inspectIdentity(statusJob{path: dest, provider: t.Provider, org: t.Org, name: r.Name, token: m.config.Providers[t.Provider].Token, fixedPath: true}, repos, probe)
		if hasStatusError(canonical) {
			return fmt.Errorf("canonical checkout: %s", statusErrorMessage(canonical))
		}
		if canonical.RepositoryID != s.RepositoryID || canonical.IdentityIssue != "" || canonical.Orphan || canonical.Transferred {
			return archiveSkip("destination does not verify as the same repository")
		}
		if err := m.confirmStatusIdentity(canonical); err != nil {
			return err
		}
		return confirmCheckoutDirectory(canonical)
	}
	if err := verify(); err != nil {
		return err
	}
	key := orgKey{t.Provider, t.Org}.string()
	match, err := probe.matches(s.Path, key, m.config.Providers[t.Provider].Token, m.config.Providers[t.Provider].Options.Clone.Protocol, r)
	if err != nil {
		return err
	}
	if !match {
		return archiveSkip("local history does not match the renamed upstream")
	}
	namespace := probe.fetched[fmt.Sprintf("%s/%d", key, s.RepositoryID)]
	objects, err := gitOutput(probe.dir, "for-each-ref", "--format=%(objectname)", namespace)
	if err != nil {
		return err
	}
	localOnly, err := probe.localOnly(s.Path, strings.Fields(objects))
	if err != nil {
		return err
	}
	if localOnly.count > 0 {
		return archiveSkip(localOnly.reason())
	}
	// Recheck source work and destination identity immediately before deletion.
	if err := verify(); err != nil {
		return err
	}
	localOnly, err = probe.localOnly(s.Path, strings.Fields(objects))
	if err != nil {
		return err
	}
	if localOnly.count > 0 {
		return archiveSkip(localOnly.reason())
	}
	if err := os.RemoveAll(s.Path); err != nil {
		return fmt.Errorf("removing obsolete checkout: %w", err)
	}
	return nil
}
