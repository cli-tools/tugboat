package repo

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gitea.swiftstrike.ai/swiftstrike/tugboat/internal/cache"
	"gitea.swiftstrike.ai/swiftstrike/tugboat/internal/config"
	"gitea.swiftstrike.ai/swiftstrike/tugboat/internal/remote"
)

// Cache only history-proven identities at an unchanged local HEAD. Git config,
// shallow state and directory metadata invalidate reused or modified checkouts.
func (m *Manager) identityCacheKey(dir, origin string, p config.Provider) string {
	if m.Cache == nil {
		return ""
	}
	head, err := gitOutput(dir, "rev-parse", "--verify", "HEAD")
	if err != nil || strings.TrimSpace(head) == "" {
		return ""
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	info, err := os.Lstat(filepath.Join(dir, ".git"))
	if err != nil || !info.IsDir() {
		return ""
	}
	parts := []string{"local-original-history-v1", abs, p.Type, providerIdentityURL(p.APIURL), normalizeGitRemoteURL(origin), head, fmt.Sprint(info.ModTime().UnixNano())}
	for _, name := range []string{"HEAD", "config", "shallow"} {
		data, err := os.ReadFile(filepath.Join(dir, ".git", name))
		if err != nil && !(name == "shallow" && os.IsNotExist(err)) {
			return ""
		}
		parts = append(parts, string(data))
	}
	return cache.Key(parts...)
}

const identityCacheAge = 30 * 24 * time.Hour

// Scheduling and verification must recognize the same cached identities. A
// checkout's explicit metadata is always authoritative, including read errors.
func (m *Manager) loadCheckoutIdentity(dir, origin string, p config.Provider) (*checkoutIdentity, string, error) {
	id, err := readIdentity(dir)
	if err != nil || id != nil {
		return id, "", err
	}
	key := m.identityCacheKey(dir, origin, p)
	var cached checkoutIdentity
	if key != "" && !m.RefreshCache && m.Cache.Read(key, identityCacheAge, &cached) &&
		cached.Version == 1 && cached.RepositoryID > 0 && cached.FullName != "" && cached.Pending == nil &&
		cached.ProviderType == p.Type && cached.APIURL == providerIdentityURL(p.APIURL) && cached.Origin == normalizeGitRemoteURL(origin) {
		return &cached, "", nil
	}
	return nil, key, nil
}

// Listing results include unresolved legacy checkouts, whose archive probes
// would otherwise repeat on every invocation. They never authorize mutations.
func (m *Manager) inspectListedIdentity(job statusJob, repos map[string]remote.Repository, probe *historyProbe) RepoStatus {
	return m.inspectReadOnlyIdentity(job, repos, probe, false)
}

func (m *Manager) inspectReadOnlyIdentity(job statusJob, repos map[string]remote.Repository, probe *historyProbe, forStatus bool) RepoStatus {
	key := ""
	if m.Cache != nil {
		origin, err := checkoutOrigin(job.path)
		if err == nil && !job.missing {
			p := m.config.Providers[job.provider]
			fingerprint := m.identityCacheKey(job.path, origin, p)
			metadata, metadataErr := json.Marshal(repos)
			identity, identityErr := os.ReadFile(identityPath(job.path))
			if fingerprint != "" && metadataErr == nil && (identityErr == nil || os.IsNotExist(identityErr)) {
				key = cache.Key("listing-local", fingerprint, job.target, job.provider, job.org, job.name,
					job.path, fmt.Sprint(job.fixedPath), cache.Key(p.Type, p.APIURL, p.Token), string(metadata), string(identity))
			}
		}
	}
	var s RepoStatus
	if key != "" && !m.RefreshCache && m.Cache.Read(key, remote.ListingCacheAge, &s) {
		// Status needs live identity objects for its fetch checks. Unresolved
		// identities cannot fetch, so their read-only discovery result suffices.
		if !forStatus || s.RepositoryID == 0 {
			return s
		}
	}
	s = m.inspectIdentity(job, repos, probe)
	if key != "" && s.Error == "" {
		m.Cache.Write(key, s)
	}
	return s
}

// A live advertisement ties a locally available commit to the current remote.
// Missing objects or any inconclusive result use the isolated history probe.
func (p *historyProbe) advertisedHistoryMatches(dir, token, protocol string, r remote.Repository) bool {
	advertisementDir, err := p.advertisementDirectory()
	if err != nil {
		return false
	}
	// Match the history probe's configuration scope. A checkout-local
	// insteadOf rule must not substitute another repository's advertisement.
	refs, err := gitOutputWithAuth(advertisementDir, token, "--git-dir="+advertisementDir,
		"ls-remote", "--heads", "--tags", pickCloneURL(&r, protocol))
	if err != nil {
		return false
	}
	seen := make(map[string]bool)
	for _, line := range strings.Split(refs, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || seen[fields[0]] {
			continue
		}
		seen[fields[0]] = true
		if _, err := originalHistoryMergeBase(dir, "HEAD", fields[0]+"^{commit}"); err == nil {
			return true
		}
	}
	return false
}

func (p *historyProbe) advertisementDirectory() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.advertisementDir != "" {
		return p.advertisementDir, nil
	}
	dir, err := os.MkdirTemp("", "tugboat-advertisement-")
	if err != nil {
		return "", err
	}
	if _, err := gitOutput(dir, "init", "--bare", "--quiet"); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	p.advertisementDir = dir
	return dir, nil
}

// Identity follows immutable commit objects, not checkout-local replacement
// refs or grafts (including graft files inherited through the environment).
func originalHistoryMergeBase(dir, left, right string) (string, error) {
	cmd := exec.Command("git", "--no-replace-objects", "merge-base", left, right)
	cmd.Dir = dir
	cmd.Env = append(gitEnvNoPrompt(), "GIT_GRAFT_FILE="+os.DevNull)
	out, err := cmd.Output()
	return string(out), err
}
