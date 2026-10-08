package repo

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"gitea.swiftstrike.ai/swiftstrike/tugboat/internal/config"
	"gitea.swiftstrike.ai/swiftstrike/tugboat/internal/remote"
)

const gitConfigLockMarker = "TUGBOAT-GIT-CONFIG-LOCK-V1\n"

// Called with the checkout lock held. Only Tugboat's complete marker is
// removable after a crash; ordinary Git config locks are left alone.
func cleanupGitConfigLock(dir string) error {
	filename := filepath.Join(dir, ".git", "config.lock")
	info, err := os.Lstat(filename)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != int64(len(gitConfigLockMarker)) {
		return nil
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		return err
	}
	if string(data) != gitConfigLockMarker {
		return nil
	}
	current, err := os.Lstat(filename)
	if err != nil {
		return err
	}
	if !os.SameFile(info, current) {
		return fmt.Errorf("Git config lock changed during recovery")
	}
	return os.Remove(filename)
}

// Prepare both origin URLs privately, then replace the live config once. A
// crash can leave either complete config, never a mismatched fetch/push pair.
func rewriteOriginConfig(dir string, before []byte, mode os.FileMode, fetch, push string) error {
	configPath := filepath.Join(dir, ".git", "config")
	f, err := os.CreateTemp(filepath.Dir(configPath), ".tugboat-config-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(before)
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err := gitRun(dir, "config", "--file", f.Name(), "--replace-all", "remote.origin.url", fetch); err != nil {
		return err
	}
	if push != "" {
		if err := gitRun(dir, "config", "--file", f.Name(), "--replace-all", "remote.origin.pushurl", push); err != nil {
			return err
		}
	}
	// Link a fully written marker atomically. Even a crash immediately after
	// reservation leaves a recognizable lock that the checkout lock can recover.
	marker, err := os.CreateTemp(filepath.Dir(configPath), ".tugboat-config-lock-")
	if err != nil {
		return err
	}
	defer os.Remove(marker.Name())
	_, err = marker.WriteString(gitConfigLockMarker)
	if err == nil {
		err = marker.Sync()
	}
	err = errors.Join(err, marker.Close())
	if err != nil {
		return err
	}
	if err := os.Link(marker.Name(), configPath+".lock"); err != nil {
		return err
	}
	defer os.Remove(configPath + ".lock")
	current, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, before) {
		return fmt.Errorf("Git config changed during reconciliation")
	}
	prepared, err := os.Open(f.Name())
	if err != nil {
		return err
	}
	err = errors.Join(prepared.Sync(), prepared.Close())
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), configPath)
}

func (m *Manager) confirmRepository(provider, org string, r remote.Repository) error {
	client, ok := m.providers[provider]
	if !ok {
		return fmt.Errorf("no client for provider %s", provider)
	}
	org = repositoryOwner(r, org)
	current, err := client.GetRepo(org, r.Name)
	if err != nil {
		return err
	}
	if current == nil || current.ID <= 0 || current.ID != r.ID || current.Name != r.Name ||
		(current.FullName != "" && !strings.EqualFold(current.FullName, org+"/"+r.Name)) {
		return fmt.Errorf("provider identity changed for %s/%s; retry after refreshing metadata", org, r.Name)
	}
	return nil
}

func (m *Manager) confirmStatusIdentity(s RepoStatus) error {
	if s.repository == nil || s.identity == nil || s.RepositoryID <= 0 || s.IdentityIssue != "" {
		return fmt.Errorf("repository identity is unresolved")
	}
	if err := confirmCheckoutDirectory(s); err != nil {
		return err
	}
	stored, err := readIdentity(s.Path)
	if err != nil {
		return err
	}
	if stored != nil && (stored.RepositoryID != s.RepositoryID || stored.Pending != nil || stored.ProviderType != s.identity.ProviderType || stored.APIURL != s.identity.APIURL) {
		return fmt.Errorf("local repository identity changed during the command")
	}
	origin, err := checkoutOrigin(s.Path)
	if err != nil {
		return err
	}
	if !originMatches(origin, *s.repository) {
		return fmt.Errorf("origin changed during the command")
	}
	return m.confirmRepository(s.Provider, s.Org, *s.repository)
}

func confirmCheckoutDirectory(s RepoStatus) error {
	current, err := os.Lstat(filepath.Join(s.Path, ".git"))
	if err != nil {
		return err
	}
	if !current.IsDir() || s.gitDirInfo != nil && !os.SameFile(s.gitDirInfo, current) {
		return fmt.Errorf("checkout directory changed during the command")
	}
	return nil
}

// All identity adoption uses the same checkout lock as relocation. Existing
// metadata is verified rather than rewritten, so stale scans cannot erase a
// recovery marker written by another Tugboat process.
func (m *Manager) saveVerifiedIdentity(s RepoStatus) error {
	if runtime.GOOS == "linux" {
		unlock, err := lockCheckout(s.Path)
		if err != nil {
			return err
		}
		defer unlock()
	}
	if err := m.confirmStatusIdentity(s); err != nil {
		return err
	}
	stored, err := readIdentity(s.Path)
	if err != nil {
		return err
	}
	if stored != nil {
		return nil
	}
	return writeIdentity(s.Path, s.identity)
}

// Stage every new checkout under an owned holder directory. Publishing uses a
// no-replace rename so an existing checkout or user-created path is never lost.
func (m *Manager) stageClone(provider, org string, r remote.Repository, parent string) (string, string, error) {
	if !safeRepoName(r.Name) || r.ID <= 0 {
		return "", "", fmt.Errorf("invalid provider repository name or ID")
	}
	if err := m.confirmRepository(provider, org, r); err != nil {
		return "", "", err
	}
	holder, err := os.MkdirTemp(parent, ".tugboat-clone-")
	if err != nil {
		return "", "", err
	}
	dest := filepath.Join(holder, "checkout")
	err = m.cloneInto(provider, org, r, dest)
	if err != nil {
		_ = os.RemoveAll(holder)
		return "", "", err
	}
	return holder, dest, nil
}

func (m *Manager) cloneInto(provider, org string, r remote.Repository, dest string) error {
	p := m.config.Providers[provider]
	cloneURL := pickCloneURL(&r, p.Options.Clone.Protocol)
	cmd := exec.Command("git", "clone", "--quiet", "--", cloneURL, dest)
	cmd.Env = gitEnvWithAuth(p.Token)
	out, err := cmd.CombinedOutput()
	if err == nil {
		err = m.confirmRepository(provider, org, r)
	}
	if err == nil {
		err = writeIdentity(dest, newIdentity(p, org, r, cloneURL))
	}
	if err != nil {
		return fmt.Errorf("cloning %s/%s: %w: %s", org, r.Name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (m *Manager) cloneVerified(provider, org string, r remote.Repository, dest string) error {
	if runtime.GOOS != "linux" {
		// Preserve ordinary cloning on platforms without reconciliation's
		// atomic directory primitive. Git itself creates the destination and
		// refuses populated paths. Automatic moves still fail closed there.
		if err := requireAbsent(dest); err != nil {
			return err
		}
		if !safeRepoName(r.Name) || r.ID <= 0 {
			return fmt.Errorf("invalid provider repository name or ID")
		}
		if err := m.confirmRepository(provider, org, r); err != nil {
			return err
		}
		return m.cloneInto(provider, org, r, dest)
	}
	holder, staged, err := m.stageClone(provider, org, r, filepath.Dir(dest))
	if err != nil {
		return err
	}
	defer os.RemoveAll(holder)
	return renameNoReplace(staged, dest)
}

func replacementEligible(t config.Target, r remote.Repository, excludeEmpty, includeArchived bool) bool {
	if r.Archived && !includeArchived || r.Empty && excludeEmpty {
		return false
	}
	for _, pattern := range t.Exclude {
		if match, _ := path.Match(pattern, r.Name); match {
			return false
		}
	}
	return true
}

func renamedOrigin(old string, r remote.Repository) (string, error) {
	parsed, _ := url.Parse(old)
	ssh := parsed != nil && parsed.Scheme == "ssh" || !strings.Contains(old, "://") && strings.Contains(old, "@")
	if ssh {
		if r.SSHURL == "" {
			return "", archiveSkip("renamed repository has no SSH clone URL")
		}
		return r.SSHURL, nil
	}
	if r.CloneURL == "" {
		return "", archiveSkip("renamed repository has no clone URL")
	}
	return r.CloneURL, nil
}

func checkMoveSafety(dir string, t config.Target) error {
	if err := validateArchiveRemovalPath(dir, t); err != nil {
		return archiveSkip(err.Error())
	}
	gitDir, err := os.Lstat(filepath.Join(dir, ".git"))
	if err != nil || !gitDir.IsDir() {
		return archiveSkip("linked or symlinked Git directory")
	}
	if worktree, _ := gitOutput(dir, "config", "--get", "core.worktree"); strings.TrimSpace(worktree) != "" {
		return archiveSkip("explicit core.worktree path")
	}
	if op, err := activeGitOperation(dir); err != nil {
		return err
	} else if op != "" {
		return archiveSkip("active Git operation: " + op)
	}
	if linked, err := hasLinkedWorktree(dir); err != nil {
		return err
	} else if linked {
		return archiveSkip("has linked worktrees")
	}
	if nested, err := nestedGitCheckout(dir); err != nil {
		return err
	} else if nested != "" {
		return archiveSkip("contains nested Git checkout " + nested)
	}
	return nil
}

func requireAbsent(filename string) error {
	if _, err := os.Lstat(filename); err == nil {
		return archiveSkip("destination is occupied: " + filename)
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Pending metadata travels with the preserved checkout. Recovery never relies
// on a leftover staging directory: it can safely restage from the recorded ID.
func (m *Manager) reconcileCheckout(t config.Target, s RepoStatus, repos map[string]remote.Repository, excludeEmpty, includeArchived bool) (bool, error) {
	if s.Transferred {
		return false, archiveSkip(s.IdentityIssue)
	}
	if s.identity == nil || s.repository == nil || s.Orphan || hasStatusError(s) {
		return false, nil
	}
	r := *s.repository
	dest := filepath.Join(t.Path, r.Name)
	move := filepath.Clean(dest) != filepath.Clean(s.Path)
	origin, err := checkoutOrigin(s.Path)
	if err != nil {
		return false, err
	}
	fixOrigin := !originMatches(origin, r)
	pending := s.identity.Pending
	if pending == nil && move {
		if replacement, ok := repos[s.Name]; ok && replacement.ID != r.ID && replacementEligible(t, replacement, excludeEmpty, includeArchived) {
			pending = &pendingReplacement{Name: replacement.Name, RepositoryID: replacement.ID}
		}
	}
	if !move && !fixOrigin && pending == nil {
		return false, m.saveVerifiedIdentity(s)
	}
	unlock, err := lockCheckout(s.Path)
	if err != nil {
		return false, archiveSkip("checkout is busy: " + err.Error())
	}
	defer unlock()
	if err := confirmCheckoutDirectory(s); err != nil {
		return false, archiveSkip(err.Error())
	}
	stored, err := readIdentity(s.Path)
	if err != nil {
		return false, err
	}
	if stored != nil && (stored.RepositoryID != s.RepositoryID || stored.ProviderType != s.identity.ProviderType || stored.APIURL != s.identity.APIURL) {
		return false, archiveSkip("checkout identity changed during reconciliation")
	}
	if stored != nil {
		a, b := stored.Pending, s.identity.Pending
		if (a == nil) != (b == nil) || a != nil && *a != *b {
			return false, archiveSkip("replacement recovery state changed during reconciliation")
		}
	}
	currentOrigin, err := checkoutOrigin(s.Path)
	if err != nil {
		return false, err
	}
	if currentOrigin != origin {
		return false, archiveSkip("origin changed during reconciliation")
	}
	if err := checkMoveSafety(s.Path, t); err != nil {
		return false, err
	}
	if move {
		if err := requireAbsent(dest); err != nil {
			return false, err
		}
	}
	if err := m.confirmRepository(t.Provider, t.Org, r); err != nil {
		return false, err
	}
	newOrigin, err := renamedOrigin(origin, r)
	if err != nil {
		return false, err
	}
	newPushOrigin := ""
	if _, err := gitOutput(s.Path, "config", "--local", "--get", "remote.origin.pushurl"); err == nil {
		push, err := gitOutput(s.Path, "remote", "get-url", "--push", "origin")
		if err != nil {
			return false, err
		}
		newPushOrigin, err = renamedOrigin(strings.TrimSpace(push), r)
		if err != nil {
			return false, err
		}
	}

	var holder, staged string
	if pending != nil {
		replacement, ok := repos[pending.Name]
		if !ok || replacement.ID != pending.RepositoryID {
			return false, fmt.Errorf("pending replacement identity changed for %s", pending.Name)
		}
		if !replacementEligible(t, replacement, excludeEmpty, includeArchived) {
			return false, archiveSkip("pending replacement is excluded by clone filters")
		}
		replacementPath := filepath.Join(t.Path, pending.Name)
		if !move || replacementPath != s.Path {
			if _, err := os.Lstat(replacementPath); err == nil {
				id, readErr := readIdentity(replacementPath)
				if readErr != nil || id == nil || id.RepositoryID != replacement.ID || id.ProviderType != s.identity.ProviderType || id.APIURL != s.identity.APIURL {
					return false, archiveSkip("pending replacement destination is occupied: " + replacementPath)
				}
				if _, err := checkoutOrigin(replacementPath); err != nil {
					return false, err
				}
				if err := verifyOriginMatches(replacementPath, &replacement); err != nil {
					return false, err
				}
				if err := m.confirmRepository(t.Provider, t.Org, replacement); err != nil {
					return false, err
				}
				// Publishing succeeded before a previous interruption.
				pending = nil
			} else if !os.IsNotExist(err) {
				return false, err
			}
		}
		if pending != nil {
			// Reuse a staged checkout after interruption, but only after its
			// scoped identity and origin have been verified again.
			if pending.Staging != "" {
				holder = filepath.Join(t.Path, pending.Staging)
				staged = filepath.Join(holder, "checkout")
				info, statErr := os.Lstat(holder)
				if statErr == nil {
					if !info.IsDir() {
						return false, fmt.Errorf("replacement staging path is not a directory")
					}
					for _, dir := range []string{staged, filepath.Join(staged, ".git")} {
						info, err := os.Lstat(dir)
						if err != nil || !info.IsDir() {
							return false, fmt.Errorf("replacement staging checkout is not a real directory")
						}
					}
					id, readErr := readIdentity(staged)
					if readErr != nil || id == nil || id.RepositoryID != replacement.ID || id.ProviderType != s.identity.ProviderType || id.APIURL != s.identity.APIURL {
						return false, fmt.Errorf("replacement staging identity is invalid")
					}
					if err := verifyOriginMatches(staged, &replacement); err != nil {
						return false, err
					}
					if err := m.confirmRepository(t.Provider, t.Org, replacement); err != nil {
						return false, err
					}
				} else if os.IsNotExist(statErr) {
					holder, staged = "", ""
				} else {
					return false, statErr
				}
			}
			if staged == "" {
				holder, staged, err = m.stageClone(t.Provider, t.Org, replacement, t.Path)
			}
			if err != nil {
				return false, err
			}
			copy := *pending
			copy.Staging = filepath.Base(holder)
			pending = &copy
			defer os.RemoveAll(holder)
		}
	}
	// Snapshot exact files for rollback, including origins with user settings.
	configPath := filepath.Join(s.Path, ".git", "config")
	configInfo, err := os.Stat(configPath)
	if err != nil {
		return false, err
	}
	configData, err := os.ReadFile(configPath)
	if err != nil {
		return false, err
	}
	identityData, identityErr := os.ReadFile(identityPath(s.Path))
	if identityErr != nil && !os.IsNotExist(identityErr) {
		return false, identityErr
	}
	currentPath := s.Path
	configChanged := false
	rollback := func(cause error) (bool, error) {
		if currentPath != s.Path {
			if err := renameNoReplace(currentPath, s.Path); err != nil {
				return false, errors.Join(cause, fmt.Errorf("rollback retained checkout at %s: %w", currentPath, err))
			}
		}
		var configErr error
		if configChanged {
			configErr = atomicWrite(filepath.Join(s.Path, ".git", "config"), configData, configInfo.Mode().Perm())
		}
		var restoreErr error
		if os.IsNotExist(identityErr) {
			restoreErr = os.Remove(identityPath(s.Path))
			if os.IsNotExist(restoreErr) {
				restoreErr = nil
			}
		} else {
			restoreErr = atomicWrite(identityPath(s.Path), identityData, 0600)
		}
		return false, errors.Join(cause, configErr, restoreErr)
	}
	id := *s.identity
	id.Pending = pending
	if err := writeIdentity(s.Path, &id); err != nil {
		return false, err
	}
	if fixOrigin {
		if err := rewriteOriginConfig(s.Path, configData, configInfo.Mode().Perm(), newOrigin, newPushOrigin); err != nil {
			return rollback(err)
		}
		configChanged = true
	}
	id.FullName, id.Origin = t.Org+"/"+r.Name, normalizeGitRemoteURL(newOrigin)
	if err := writeIdentity(s.Path, &id); err != nil {
		return rollback(err)
	}
	if move {
		if err := renameNoReplace(s.Path, dest); err != nil {
			return rollback(err)
		}
		currentPath = dest
	}
	if staged != "" {
		replacement := repos[pending.Name]
		if err := m.confirmRepository(t.Provider, t.Org, replacement); err != nil {
			return rollback(err)
		}
		if err := renameNoReplace(staged, filepath.Join(t.Path, pending.Name)); err != nil {
			return rollback(err)
		}
	}
	if move && m.Verbose {
		fmt.Printf("  [RENAMED] %s -> %s\n", s.Path, currentPath)
	} else if fixOrigin && m.Verbose {
		fmt.Printf("  [ORIGIN] %s -> %s/%s\n", currentPath, t.Org, r.Name)
	}
	if staged != "" && m.Verbose {
		fmt.Printf("  [CLONED] %s/%s (replacement)\n", t.Org, pending.Name)
	}
	id.Pending = nil
	// Once published, leave both checkouts in place if clearing the marker
	// fails. The next run will recognize the replacement's saved identity.
	if err := writeIdentity(currentPath, &id); err != nil {
		return true, err
	}
	return true, nil
}

func (m *Manager) reconcileOrg(t config.Target, repos map[string]remote.Repository, probe *historyProbe, excludeEmpty, includeArchived bool, removeTransferred ...bool) map[string]error {
	return m.reconcileOrgReady(t, repos, probe, excludeEmpty, includeArchived, len(removeTransferred) > 0 && removeTransferred[0], false, nil, nil)
}

func (m *Manager) reconcileOrgReady(t config.Target, repos map[string]remote.Repository, probe *historyProbe, excludeEmpty, includeArchived, retire, removeDuplicates bool, done func(string) bool, ready func(string, error)) map[string]error {
	failures := make(map[string]error)
	entries, err := os.ReadDir(t.Path)
	if os.IsNotExist(err) {
		return failures
	}
	if err != nil {
		failures[t.Path] = err
		return failures
	}
	for _, entry := range entries {
		dir := filepath.Join(t.Path, entry.Name())
		if !entry.IsDir() || !isGitRepo(dir) || done != nil && done(dir) {
			continue
		}
		probe.progress.detail("  [DETAIL] %s: verifying repository identity\n", dir)
		s := m.inspectIdentity(statusJob{path: dir, target: t.Name, provider: t.Provider, org: t.Org, name: entry.Name(), token: m.config.Providers[t.Provider].Token}, repos, probe)
		if hasStatusError(s) {
			failures[dir] = errors.New(statusErrorMessage(s))
			if ready != nil {
				ready(dir, failures[dir])
			}
			continue
		}
		if s.Transferred {
			if !retire {
				failures[dir] = errors.New(s.IdentityIssue)
			} else if err := m.removeTransferredCheckout(t, s, repos, probe); err != nil {
				var skip *archiveSkipError
				if errors.As(err, &skip) {
					probe.progress.note(dir, skip.reason)
				} else {
					failures[dir] = fmt.Errorf("%s: %w", s.IdentityIssue, err)
				}
			}
			if ready != nil {
				ready(dir, failures[dir])
			}
			continue
		}
		if removeDuplicates && s.ReplacementID == 0 && s.repository != nil && s.repository.Name != s.Name && isGitRepo(filepath.Join(t.Path, s.repository.Name)) {
			err := m.removeRenamedDuplicate(t, s, repos, probe)
			if err != nil {
				var skip *archiveSkipError
				if errors.As(err, &skip) {
					probe.progress.note(dir, skip.reason)
				} else {
					failures[dir] = err
				}
			} else {
				probe.progress.note(dir, "removed duplicate of "+t.Org+"/"+s.repository.Name+"; kept "+filepath.Join(t.Path, s.repository.Name))
				if probe.progress.removed == nil {
					probe.progress.removed = make(map[string]bool)
				}
				probe.progress.removed[dir] = true
			}
			if ready != nil {
				ready(dir, failures[dir])
			}
			continue
		}
		changed, err := m.reconcileCheckout(t, s, repos, excludeEmpty, includeArchived)
		if changed && s.repository != nil {
			dest := filepath.Join(t.Path, s.repository.Name)
			probe.progress.plan(dest)
			if dest != dir {
				if !isGitRepo(dir) && err == nil {
					probe.progress.relocate(dir, dest)
				}
				probe.progress.note(dest, "preserved from "+dir)
			} else if s.identity.Pending != nil {
				probe.progress.note(dest, "replacement recovery completed")
			} else {
				probe.progress.note(dest, "origin updated")
			}
			replacementName := s.Name
			if s.identity.Pending != nil {
				replacementName = s.identity.Pending.Name
			}
			if replacement, ok := repos[replacementName]; ok && replacement.ID != s.RepositoryID {
				replacementPath := filepath.Join(t.Path, replacementName)
				if id, err := readIdentity(replacementPath); err == nil && id != nil && id.RepositoryID == replacement.ID {
					probe.progress.note(replacementPath, "replacement clone ready")
				}
			}
		}
		if err != nil {
			var skip *archiveSkipError
			if errors.As(err, &skip) {
				probe.progress.note(dir, skip.reason)
				if !removeDuplicates && s.ReplacementID == 0 && s.repository != nil && s.repository.Name != s.Name && isGitRepo(filepath.Join(t.Path, s.repository.Name)) {
					probe.progress.note(dir, "use --remove-archived for safe duplicate cleanup")
				}
				probe.progress.detail("  [SKIP] %s: %s\n", dir, skip.reason)
			} else {
				failures[dir] = err
			}
		} else if s.IdentityIssue != "" && s.identity == nil {
			probe.progress.detail("  [SKIP] %s: %s\n", dir, s.IdentityIssue)
		}
		if ready != nil {
			if isGitRepo(dir) || failures[dir] != nil {
				ready(dir, failures[dir])
			}
			if changed && s.repository != nil {
				dest := filepath.Join(t.Path, s.repository.Name)
				if dest != dir && isGitRepo(dest) {
					ready(dest, nil)
				}
			}
		}
	}
	return failures
}
