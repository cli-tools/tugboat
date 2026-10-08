package repo

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gitea.swiftstrike.ai/swiftstrike/tugboat/internal/config"
	"gitea.swiftstrike.ai/swiftstrike/tugboat/internal/remote"
)

// Archived transfers leave maintenance and may be discarded during receiving
// sync, with the same guards as ordinary archived cleanup. Prepare any
// replacement first so clone failures cannot destroy the original checkout.
func (m *Manager) removeTransferredCheckout(t config.Target, s RepoStatus, repos map[string]remote.Repository, probe *historyProbe) error {
	if s.repository == nil || s.identity == nil || !s.Archived || s.identity.Pending != nil {
		return archiveSkip("transferred repository is not an identified archive")
	}
	unlock, err := lockCheckout(s.Path)
	if err != nil {
		return err
	}
	defer unlock()
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
		return fmt.Errorf("checkout identity changed during cleanup")
	}
	origin, err := checkoutOrigin(s.Path)
	if err != nil {
		return err
	}
	if normalizeGitRemoteURL(origin) != s.identity.Origin && !originMatches(origin, *s.repository) {
		return fmt.Errorf("origin changed during cleanup")
	}
	// Refuse dirty work and stashes before any origin or identity changes.
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
	if err := m.confirmRepository(t.Provider, t.Org, *s.repository); err != nil {
		return err
	}
	key := orgKey{t.Provider, t.Org}.string()
	match, err := probe.matches(s.Path, key, m.config.Providers[t.Provider].Token, m.config.Providers[t.Provider].Options.Clone.Protocol, *s.repository)
	if err != nil {
		return err
	}
	if !match {
		return archiveSkip("local history does not match the transferred archive")
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

	var holder, staged string
	replacement, replace := repos[s.Name]
	replace = replace && replacement.ID != s.RepositoryID && replacementEligible(t, replacement, false, false)
	if replace {
		holder, staged, err = m.stageClone(t.Provider, t.Org, replacement, t.Path)
		if err != nil {
			return err
		}
		defer os.RemoveAll(holder)
	}
	newOrigin, err := renamedOrigin(origin, *s.repository)
	if err != nil {
		return err
	}
	push := ""
	if _, err := gitOutput(s.Path, "config", "--local", "--get", "remote.origin.pushurl"); err == nil {
		oldPush, err := gitOutput(s.Path, "remote", "get-url", "--push", "origin")
		if err != nil {
			return err
		}
		push, err = renamedOrigin(strings.TrimSpace(oldPush), *s.repository)
		if err != nil {
			return err
		}
	}
	beforeIdentity, identityErr := os.ReadFile(identityPath(s.Path))
	if identityErr != nil && !os.IsNotExist(identityErr) {
		return identityErr
	}
	configPath := filepath.Join(s.Path, ".git", "config")
	before, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	info, err := os.Stat(configPath)
	if err != nil {
		return err
	}
	id := *s.identity
	id.FullName = s.repository.FullName
	if err := writeIdentity(s.Path, &id); err != nil {
		return err
	}
	if err := rewriteOriginConfig(s.Path, before, info.Mode().Perm(), newOrigin, push); err != nil {
		if os.IsNotExist(identityErr) {
			_ = os.Remove(identityPath(s.Path))
		} else {
			_ = atomicWrite(identityPath(s.Path), beforeIdentity, 0600)
		}
		return err
	}
	id.Origin = normalizeGitRemoteURL(newOrigin)
	if err := writeIdentity(s.Path, &id); err != nil {
		return err
	}
	s.identity = &id
	s.Org = repositoryOwner(*s.repository, t.Org)
	s.IdentityIssue = ""
	if _, err := m.removeArchivedRepo(s, t, m.config.Providers[t.Provider].Token); err != nil {
		return err
	}
	probe.progress.note(s.Path, fmt.Sprintf("removed transferred archive %s (ID %d)", id.FullName, id.RepositoryID))
	if replace {
		if err := m.confirmRepository(t.Provider, t.Org, replacement); err != nil {
			return err
		}
		if err := renameNoReplace(staged, s.Path); err != nil {
			return err
		}
		probe.progress.note(s.Path, "replacement clone ready")
	} else {
		if probe.progress.removed == nil {
			probe.progress.removed = make(map[string]bool)
		}
		probe.progress.removed[s.Path] = true
	}
	return nil
}
