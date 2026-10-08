package repo

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"gitea.swiftstrike.ai/swiftstrike/tugboat/internal/config"
	"gitea.swiftstrike.ai/swiftstrike/tugboat/internal/remote"
)

// Identity belongs to a provider instance, not a mutable repository name or
// configuration alias. This file is local Git metadata, never a worktree file.
type checkoutIdentity struct {
	Version      int                 `json:"version"`
	ProviderType string              `json:"provider_type"`
	APIURL       string              `json:"api_url"`
	RepositoryID int64               `json:"repository_id"`
	FullName     string              `json:"full_name"`
	Origin       string              `json:"origin"`
	Pending      *pendingReplacement `json:"pending_replacement,omitempty"`
}

type pendingReplacement struct {
	Name         string `json:"name"`
	RepositoryID int64  `json:"repository_id"`
	Staging      string `json:"staging,omitempty"`
}

func identityPath(dir string) string { return filepath.Join(dir, ".git", "tugboat.json") }

func providerIdentityURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	u.User, u.RawQuery, u.Fragment = nil, "", ""
	u.Host = strings.ToLower(u.Host)
	return strings.TrimSuffix(u.String(), "/")
}

func readIdentity(dir string) (*checkoutIdentity, error) {
	filename := identityPath(dir)
	info, err := os.Lstat(filename)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("identity metadata is not a regular file")
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	var id checkoutIdentity
	if err := json.Unmarshal(data, &id); err != nil {
		return nil, fmt.Errorf("reading repository identity: %w", err)
	}
	if id.Version != 1 || id.RepositoryID <= 0 || id.ProviderType == "" || id.APIURL == "" || id.Origin == "" || id.FullName == "" {
		return nil, fmt.Errorf("invalid or unsupported repository identity")
	}
	if id.Pending != nil && (!safeRepoName(id.Pending.Name) || id.Pending.RepositoryID <= 0) {
		return nil, fmt.Errorf("invalid pending replacement identity")
	}
	if id.Pending != nil && id.Pending.Staging != "" && (!safeRepoName(id.Pending.Staging) || !strings.HasPrefix(id.Pending.Staging, ".tugboat-clone-")) {
		return nil, fmt.Errorf("invalid replacement staging directory")
	}
	return &id, nil
}

func writeIdentity(dir string, id *checkoutIdentity) error {
	data, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(identityPath(dir), append(data, '\n'), 0600)
}

func atomicWrite(filename string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(filename), ".tugboat-write-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), filename)
}

func newIdentity(p config.Provider, org string, r remote.Repository, origin string) *checkoutIdentity {
	return &checkoutIdentity{Version: 1, ProviderType: p.Type, APIURL: providerIdentityURL(p.APIURL),
		RepositoryID: r.ID, FullName: repositoryOwner(r, org) + "/" + r.Name, Origin: normalizeGitRemoteURL(origin)}
}

func safeRepoName(name string) bool {
	return name != "" && name != "." && name != ".." && name != ".git" && !strings.ContainsAny(name, "/\\\x00")
}

// Verify both effective fetch and push URLs, including explicit pushurl values.
// A custom push destination must not silently receive commits during sync.
func checkoutOrigin(dir string) (string, error) {
	out, err := gitOutput(dir, "remote", "get-url", "--all", "origin")
	if err != nil {
		return "", fmt.Errorf("reading origin: %w", err)
	}
	fetch := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	pushOut, err := gitOutput(dir, "remote", "get-url", "--push", "--all", "origin")
	if err != nil {
		return "", fmt.Errorf("reading origin push URL: %w", err)
	}
	push := strings.Split(strings.TrimSuffix(pushOut, "\n"), "\n")
	if len(fetch) != 1 || len(push) != 1 || normalizeGitRemoteURL(fetch[0]) != normalizeGitRemoteURL(push[0]) {
		return "", fmt.Errorf("origin has multiple or conflicting fetch/push URLs")
	}
	return fetch[0], nil
}

func originMatches(origin string, r remote.Repository) bool {
	for _, raw := range []string{r.CloneURL, r.SSHURL, r.HTMLURL} {
		if raw != "" && normalizeGitRemoteURL(origin) == normalizeGitRemoteURL(raw) {
			return true
		}
	}
	return false
}

// An old repository URL can redirect to its current name. Trust it only after
// the provider confirms the same ID within the configured organization; a URL
// similarity alone never authorizes fetching into the checkout.
func (m *Manager) originRedirectMatches(job statusJob, origin string, r remote.Repository) (bool, error) {
	normalized := normalizeGitRemoteURL(origin)
	oldName := strings.TrimSuffix(normalized[strings.LastIndex(normalized, "/")+1:], ".git")
	if !safeRepoName(oldName) || !originMatches(origin, withRepoName(r, oldName)) {
		return false, nil
	}
	redirected, err := m.providers[job.provider].GetRepo(job.org, oldName)
	if err != nil {
		return false, err
	}
	return redirected != nil && redirected.ID == r.ID && redirected.Name == r.Name &&
		(redirected.FullName == "" || strings.EqualFold(redirected.FullName, job.org+"/"+r.Name)), nil
}

// A probe owns an isolated object database. Candidate histories are fetched at
// most once per scan; local refs, tags, FETCH_HEAD and worktrees are untouched.
type historyProbe struct {
	mu            sync.Mutex
	dir           string
	fetched       map[string]string
	next          int
	progress      *progressReporter
	archives      map[string][]remote.Repository
	archiveErrors map[string]error
}

func (p *historyProbe) archivedRepos(provider string, client remote.Client) ([]remote.Repository, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.archives == nil {
		p.archives = make(map[string][]remote.Repository)
		p.archiveErrors = make(map[string]error)
	}
	if repos, ok := p.archives[provider]; ok {
		return repos, p.archiveErrors[provider]
	}
	lister, ok := client.(remote.ArchivedRepositoryLister)
	if !ok {
		return nil, nil
	}
	p.progress.detail("  [DETAIL] Loading archive metadata for %s...\n", provider)
	repos, err := lister.ListArchivedRepos()
	p.archives[provider], p.archiveErrors[provider] = repos, err
	return repos, err
}

func repositoryOwner(r remote.Repository, fallback string) string {
	parts := strings.Split(r.FullName, "/")
	if len(parts) == 2 && safeRepoName(parts[0]) && parts[1] == r.Name {
		return parts[0]
	}
	return fallback
}

func (p *historyProbe) close() {
	if p.dir != "" {
		_ = os.RemoveAll(p.dir)
	}
}

// Compare all local refs in the isolated database, where provider commits are
// available even when the checkout is behind its transferred archive.
func (p *historyProbe) localOnly(dir string, remoteObjects []string) (unpublishedWork, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.next++
	namespace := fmt.Sprintf("refs/tugboat/cleanup/%d", p.next)
	if _, err := gitOutput(p.dir, "config", "remote.local-checkout.url", dir); err != nil {
		return unpublishedWork{}, err
	}
	if _, err := gitOutput(p.dir, "-c", "uploadpack.allowFilter=true", "fetch", "--filter=tree:0", "--quiet", "--no-tags", "--no-write-fetch-head", "local-checkout", "+refs/*:"+namespace+"/*", "+HEAD:"+namespace+"/HEAD"); err != nil {
		return unpublishedWork{}, err
	}
	refs, err := gitOutput(p.dir, "for-each-ref", "--format=%(refname)", namespace)
	if err != nil {
		return unpublishedWork{}, err
	}
	var localRefs []localWorkRef
	for _, ref := range strings.Fields(refs) {
		name := strings.TrimPrefix(ref, namespace+"/")
		if strings.HasPrefix(name, "remotes/") {
			continue
		}
		label := localWorkLabel("refs/" + name)
		if name == "HEAD" {
			label = checkoutHeadLabel(dir)
		}
		localRefs = append(localRefs, localWorkRef{ref: ref, label: label})
	}
	return inspectUnpublishedWork(p.dir, localRefs, remoteObjects)
}

func (p *historyProbe) matches(dir, key, token, protocol string, r remote.Repository) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dir == "" {
		var err error
		p.dir, err = os.MkdirTemp("", "tugboat-history-")
		if err != nil {
			return false, err
		}
		if _, err = gitOutput(p.dir, "init", "--bare", "--quiet"); err != nil {
			return false, err
		}
		p.fetched = make(map[string]string)
	}
	key += fmt.Sprintf("/%d", r.ID)
	namespace, ok := p.fetched[key]
	if !ok {
		p.next++
		namespace = fmt.Sprintf("refs/tugboat/candidate/%d", p.next)
		remoteName := fmt.Sprintf("candidate-%d", p.next)
		if _, err := gitOutput(p.dir, "remote", "add", remoteName, pickCloneURL(&r, protocol)); err != nil {
			return false, err
		}
		p.progress.detail("  [DETAIL] Downloading identity history for %s (ID %d)...\n", r.FullName, r.ID)
		// Identity needs commit ancestry, not worktree content. Filtering keeps
		// large datasets and historical binaries out of this temporary database.
		if _, err := gitOutputWithAuth(p.dir, token, "fetch", "--filter=tree:0", "--quiet", "--no-tags", "--no-write-fetch-head", remoteName,
			"+refs/heads/*:"+namespace+"/heads/*", "+refs/tags/*:"+namespace+"/tags/*"); err != nil {
			return false, err
		}
		p.fetched[key] = namespace
		p.progress.detail("  [DETAIL] Identity history ready for %s\n", r.FullName)
	}
	// HEAD remains useful after someone already fetched the replacement into
	// origin/*; using only cached remote tips would misidentify that checkout.
	if _, err := gitOutput(p.dir, "config", "remote.local-checkout.url", dir); err != nil {
		return false, err
	}
	if _, err := gitOutput(p.dir, "-c", "uploadpack.allowFilter=true", "fetch", "--filter=tree:0", "--quiet", "--no-tags", "--no-write-fetch-head", "local-checkout", "+HEAD:refs/tugboat/local"); err != nil {
		return false, err
	}
	refs, err := gitOutput(p.dir, "for-each-ref", "--format=%(refname)", namespace)
	if err != nil {
		return false, err
	}
	for _, ref := range strings.Fields(refs) {
		if gitRun(p.dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}") != nil {
			continue
		}
		if _, err := gitOutput(p.dir, "merge-base", "refs/tugboat/local", ref+"^{commit}"); err == nil {
			return true, nil
		} else {
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 {
				return false, err
			}
		}
	}
	return false, nil
}

func (m *Manager) inspectIdentity(job statusJob, repos map[string]remote.Repository, probe *historyProbe) RepoStatus {
	s := RepoStatus{Path: job.path, Target: job.target, Provider: job.provider, Org: job.org, Name: job.name, Missing: job.missing}
	named, hasNamed := repos[job.name]
	if job.missing {
		if hasNamed {
			applyRemoteState(&s, named)
		}
		return s
	}
	gitDir, err := os.Lstat(filepath.Join(job.path, ".git"))
	if err != nil || !gitDir.IsDir() {
		s.Error = "checkout has a linked or symlinked Git directory"
		return s
	}
	s.gitDirInfo = gitDir
	origin, err := checkoutOrigin(job.path)
	if err != nil {
		s.Error = err.Error()
		return s
	}
	id, err := readIdentity(job.path)
	if err != nil {
		s.Error = err.Error()
		return s
	}
	p := m.config.Providers[job.provider]
	var resolved *remote.Repository
	if id != nil {
		if id.ProviderType != p.Type || id.APIURL != providerIdentityURL(p.APIURL) {
			s.IdentityIssue = "saved repository identity belongs to another provider instance"
			return s
		}
		for _, r := range repos {
			if r.ID == id.RepositoryID {
				if resolved != nil {
					s.Error = "provider returned duplicate repository IDs"
					return s
				}
				copy := r
				resolved = &copy
			}
		}
		if resolved == nil {
			parts := strings.Split(id.FullName, "/")
			if len(parts) == 2 && safeRepoName(parts[0]) && safeRepoName(parts[1]) {
				candidate, lookupErr := m.providers[job.provider].GetRepo(parts[0], parts[1])
				if lookupErr != nil {
					s.Error = lookupErr.Error()
					return s
				}
				if candidate != nil && candidate.ID == id.RepositoryID {
					resolved = candidate
				}
			}
		}
		if resolved == nil {
			archives, lookupErr := probe.archivedRepos(job.provider, m.providers[job.provider])
			if lookupErr != nil {
				s.Error = "checking archived identities: " + lookupErr.Error()
				return s
			}
			for _, r := range archives {
				if r.ID == id.RepositoryID {
					copy := r
					resolved = &copy
					break
				}
			}
		}
		if resolved == nil {
			s.Orphan = true
			s.IdentityIssue = "saved repository ID is missing from this organization"
			return s
		}
		if !originMatches(origin, *resolved) && normalizeGitRemoteURL(origin) != id.Origin {
			s.Error = "origin does not match provider repository or saved identity"
			return s
		}
	} else {
		// Ordinary active checkouts verify against their current upstream first.
		// Search archives only when that history does not match the checkout.
		candidates := make([]remote.Repository, 0, len(repos))
		for _, r := range repos {
			if r.Archived || r.Name == job.name {
				candidates = append(candidates, r)
			}
		}
		sort.Slice(candidates, func(i, j int) bool { return candidates[i].Name < candidates[j].Name })
		if hasNamed && !originMatches(origin, named) {
			redirected, err := m.originRedirectMatches(job, origin, named)
			if err != nil {
				s.Error = "checking origin redirect: " + err.Error()
				return s
			}
			if !redirected {
				s.Error = "origin does not match provider repository " + job.org + "/" + named.Name
				return s
			}
		}
		upstream, hasUpstream := named, hasNamed
		if !hasNamed {
			// Old origins must still address this provider and organization. Use
			// the old name with each canonical provider URL to validate its scope.
			trusted := false
			for _, r := range repos {
				if originMatches(origin, withRepoName(r, job.name)) {
					trusted = true
				}
			}
			if !trusted {
				s.Orphan = true
				s.IdentityIssue = "origin is outside this target; not maintained"
				return s
			}
			// Resolve an old name before scanning archives. Providers can redirect
			// this lookup to an active repository under its new name.
			redirected, lookupErr := m.providers[job.provider].GetRepo(job.org, job.name)
			if lookupErr != nil {
				s.Error = "checking repository rename: " + lookupErr.Error()
				return s
			}
			if redirected != nil {
				current, ok := repos[redirected.Name]
				if ok && current.ID == redirected.ID && current.ID > 0 && repositoryOwner(*redirected, job.org) == job.org && originMatches(origin, withRepoName(current, job.name)) {
					upstream, hasUpstream = current, true
					found := false
					for _, candidate := range candidates {
						found = found || candidate.ID == current.ID
					}
					if !found {
						candidates = append(candidates, current)
					}
				}
			}
		}
		headExists := gitRun(job.path, "rev-parse", "--verify", "--quiet", "HEAD") == nil
		shallow, _ := gitOutput(job.path, "rev-parse", "--is-shallow-repository")
		matchedUpstreamID := int64(0)
		if hasUpstream && !upstream.Archived && !upstream.Empty && headExists && strings.TrimSpace(shallow) != "true" {
			if upstream.ID <= 0 {
				s.Error = "provider returned an invalid repository ID"
				return s
			}
			match, err := probe.matches(job.path, orgKey{job.provider, job.org}.string(), job.token, p.Options.Clone.Protocol, upstream)
			if err != nil {
				s.Error = "checking upstream history: " + err.Error()
				return s
			}
			if match {
				candidates = []remote.Repository{upstream}
				matchedUpstreamID = upstream.ID
			}
		}
		if matchedUpstreamID == 0 {
			archives, lookupErr := probe.archivedRepos(job.provider, m.providers[job.provider])
			if lookupErr != nil {
				s.Error = "checking archive metadata: " + lookupErr.Error()
				return s
			}
			seen := make(map[int64]bool)
			for _, r := range candidates {
				seen[r.ID] = true
			}
			for _, r := range archives {
				if r.Archived && !seen[r.ID] {
					candidates = append(candidates, r)
					seen[r.ID] = true
				}
			}
			sort.Slice(candidates, func(i, j int) bool { return candidates[i].FullName < candidates[j].FullName })
		}
		if len(candidates) == 0 {
			s.Orphan = true
			s.IdentityIssue = "repository is missing from the provider"
			return s
		}
		var matches []remote.Repository
		for _, r := range candidates {
			if r.ID <= 0 {
				s.Error = "provider returned an invalid repository ID"
				return s
			}
			match := r.ID == matchedUpstreamID
			if match {
				// The current upstream was already verified above.
			} else if !headExists || r.Empty {
				// An unborn or locally initialized checkout can bind to its sole
				// URL candidate. Archives otherwise make history-free binding unsafe.
				match = len(candidates) == 1 && hasNamed && r.ID == named.ID
			} else if strings.TrimSpace(shallow) != "true" {
				match, err = probe.matches(job.path, orgKey{job.provider, job.org}.string(), job.token, p.Options.Clone.Protocol, r)
				if err != nil {
					s.Error = "checking repository history: " + err.Error()
					return s
				}
			}
			if match {
				matches = append(matches, r)
			}
		}
		if len(matches) != 1 {
			if len(matches) == 0 && !hasNamed && headExists && strings.TrimSpace(shallow) != "true" {
				s.Orphan = true
				s.IdentityIssue = "repository is missing from the provider; no matching archive"
				return s
			}
			listed := candidates
			if len(matches) > 1 {
				listed = matches
			}
			var labels []string
			for _, r := range listed {
				labels = append(labels, fmt.Sprintf("%s/%s (ID %d)", repositoryOwner(r, job.org), r.Name, r.ID))
			}
			if len(labels) > 5 {
				probe.progress.detail("  [DETAIL] %s: identity candidates: %s\n", job.path, strings.Join(labels, ", "))
				labels = append(labels[:5], fmt.Sprintf("%d more (use --verbose)", len(labels)-5))
			}
			s.IdentityIssue = "repository identity is ambiguous; candidates: " + strings.Join(labels, ", ") + "; identify it using .git/tugboat.json (see README)"
			return s
		}
		resolved = &matches[0]
		id = newIdentity(p, job.org, *resolved, origin)
	}
	if !safeRepoName(resolved.Name) || resolved.ID <= 0 {
		s.Error = "provider returned an invalid repository name or ID"
		return s
	}
	s.identity, s.repository = id, resolved
	applyRemoteState(&s, *resolved)
	if owner := repositoryOwner(*resolved, job.org); !strings.EqualFold(owner, job.org) {
		s.Transferred = true
		s.IdentityIssue = fmt.Sprintf("transferred to %s/%s (ID %d); outside this target's maintenance", owner, resolved.Name, resolved.ID)
		if hasNamed && named.ID != resolved.ID {
			s.ReplacementID = named.ID
		}
		return s
	}
	if resolved.Name != job.name || !originMatches(origin, *resolved) {
		s.IdentityIssue = fmt.Sprintf("renamed to %s/%s", job.org, resolved.Name)
		if job.fixedPath {
			s.IdentityIssue += "; configured path is fixed; update the repository declaration or preserve and re-clone manually"
		} else if job.target != "" {
			s.IdentityIssue += "; run tugboat sync " + job.target
		}
		if hasNamed && named.ID != resolved.ID {
			s.ReplacementID = named.ID
			s.IdentityIssue += "; local name is occupied by a replacement"
		}
	}
	if id.Pending != nil {
		s.IdentityIssue = "pending replacement " + id.Pending.Name
		if job.fixedPath {
			s.IdentityIssue += "; configured path is fixed; finish recovery manually"
		} else if job.target != "" {
			s.IdentityIssue += "; run tugboat sync " + job.target
		}
	}
	return s
}

func (m *Manager) verifyExistingClone(provider, org, name, dir string) error {
	repositories, err := m.providers[provider].ListOrgRepos(org)
	if err != nil {
		return err
	}
	repos := make(map[string]remote.Repository)
	for _, r := range repositories {
		repos[r.Name] = r
	}
	probe := &historyProbe{}
	defer probe.close()
	s := m.inspectIdentity(statusJob{path: dir, provider: provider, org: org, name: name, token: m.config.Providers[provider].Token, fixedPath: true}, repos, probe)
	if hasStatusError(s) {
		return errors.New(statusErrorMessage(s))
	}
	if s.IdentityIssue != "" {
		return &updateSkipError{reason: s.IdentityIssue}
	}
	if s.identity == nil {
		return &updateSkipError{reason: "repository identity is unresolved"}
	}
	return m.saveVerifiedIdentity(s)
}

func applyRemoteState(s *RepoStatus, r remote.Repository) {
	s.RepositoryID, s.RemoteName = r.ID, r.Name
	s.Archived, s.DefaultBranch, s.RemoteEmpty = r.Archived, r.DefaultBranch, r.Empty
	if r.Empty {
		s.Behind, s.UpstreamGone = 0, false
		if s.Unborn {
			s.Ahead = 0
		} else if s.LocalCommits > 0 {
			s.Ahead = s.LocalCommits
		}
	}
}

// Used only to recognize an old origin's scope, never to choose identity.
func withRepoName(r remote.Repository, oldName string) remote.Repository {
	rewrite := func(raw string) string {
		if raw == "" {
			return ""
		}
		suffix := ""
		if strings.HasSuffix(raw, ".git") {
			suffix = ".git"
		}
		index := strings.LastIndex(strings.TrimSuffix(raw, ".git"), "/")
		if index < 0 {
			return ""
		}
		return raw[:index+1] + oldName + suffix
	}
	r.CloneURL, r.SSHURL, r.HTMLURL = rewrite(r.CloneURL), rewrite(r.SSHURL), rewrite(r.HTMLURL)
	return r
}
