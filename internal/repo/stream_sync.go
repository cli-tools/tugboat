package repo

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gitea.swiftstrike.ai/swiftstrike/tugboat/internal/config"
	"gitea.swiftstrike.ai/swiftstrike/tugboat/internal/pool"
)

// Saved identity and canonical origin let ordinary checkouts run independently
// of the coordinator that owns rename, replacement, and retirement paths.
func (m *Manager) independentCheckout(job statusJob, metadata *scanMetadata) bool {
	if job.missing {
		return false
	}
	if job.fixedPath {
		return true
	}
	key := orgKey{job.provider, job.org}.string()
	if metadata.errors[key] != nil {
		return true
	}
	r, ok := metadata.index[key][job.name]
	if !ok {
		return false
	}
	id, err := readIdentity(job.path)
	if err != nil || id == nil || id.Pending != nil || id.RepositoryID != r.ID {
		return false
	}
	provider := m.config.Providers[job.provider]
	if id.ProviderType != provider.Type || id.APIURL != providerIdentityURL(provider.APIURL) {
		return false
	}
	origin, err := checkoutOrigin(job.path)
	return err == nil && originMatches(origin, r)
}

func (m *Manager) streamSync(targets []config.Target, opts SyncOptions, progress *progressReporter, done func(string) bool, process func(RepoStatus)) error {
	jobs, orgs, err := discoverStatusJobs(targets, m.config)
	if err != nil {
		return err
	}
	initialJobs := jobs
	metadata := &scanMetadata{}
	metadata.index, metadata.errors = m.buildRepoIndex(orgs, progress)
	progress.beginPlan(plannedSyncPaths(targets, jobs, metadata, opts.Mode))
	var scanMu sync.Mutex
	scanned := make(map[string]bool)
	scan := func(job statusJob, failure error, probe *historyProbe, prepare ...func(RepoStatus)) RepoStatus {
		scanMu.Lock()
		if scanned[job.path] || done(job.path) {
			scanMu.Unlock()
			return RepoStatus{}
		}
		scanned[job.path] = true
		scanMu.Unlock()
		s := m.scanStatus(job, metadata, probe, progress, failure).status
		for _, before := range prepare {
			before(s)
		}
		process(s)
		return s
	}
	var independent []statusJob
	for _, job := range jobs {
		if opts.Mode == SyncPush || m.independentCheckout(job, metadata) {
			independent = append(independent, job)
		}
	}
	pool.Run(independent, opts.Workers, func(job statusJob) struct{} {
		probe := &historyProbe{progress: progress}
		defer probe.close()
		scan(job, nil, probe)
		return struct{}{}
	})
	probe := &historyProbe{progress: progress}
	defer probe.close()
	reserved := make(map[string]bool)
	reserve := func(dir string) {
		reserved[dir] = true
		if !isGitRepo(dir) && !done(dir) {
			progress.forget(dir)
		}
	}
	if opts.Mode != SyncPush {
		for _, t := range targets {
			key := orgKey{t.Provider, t.Org}.string()
			if t.Repo != "" || metadata.errors[key] != nil {
				continue
			}
			progress.detail("Reconciling remaining repository identities for %s/%s...\n", t.Provider, t.Org)
			ready := func(dir string, failure error) {
				job := statusJob{path: dir, target: t.Name, provider: t.Provider, org: t.Org, name: filepath.Base(dir), token: m.config.Providers[t.Provider].Token, missing: !isGitRepo(dir)}
				scan(job, failure, probe, func(s RepoStatus) {
					if s.IdentityIssue != "" || hasStatusError(s) {
						reserve(dir)
						if s.repository != nil && strings.EqualFold(repositoryOwner(*s.repository, t.Org), t.Org) {
							reserve(filepath.Join(t.Path, s.repository.Name))
						}
						if s.identity != nil && s.identity.Pending != nil {
							reserve(filepath.Join(t.Path, s.identity.Pending.Name))
						}
					}
				})
			}
			failures := m.reconcileOrgReady(t, metadata.index[key], probe, false, false, true, opts.RemoveArchived, done, ready)
			for dir, failure := range failures {
				reserve(dir)
				if !done(dir) {
					ready(dir, failure)
				}
			}
		}
		_, err = m.ensureMissingCheckouts(targets, opts.Workers, progress, metadata, reserved, func(s RepoStatus) {
			if done(s.Path) {
				return
			}
			if hasStatusError(s) || s.Missing {
				progress.checkFinished(s.Path)
				process(s)
				return
			}
			// Each new clone is checked and updated before its worker takes another job.
			localProbe := &historyProbe{progress: progress}
			defer localProbe.close()
			scan(statusJob{path: s.Path, target: s.Target, provider: s.Provider, org: s.Org, name: s.Name, token: m.config.Providers[s.Provider].Token, fixedPath: true}, nil, localProbe)
		})
		if err != nil {
			return err
		}
	}
	// Re-read declarations after parent updates, including newly added foldouts.
	jobs, orgs, err = discoverStatusJobs(targets, m.config)
	if err != nil {
		return err
	}
	currentPaths := make(map[string]bool, len(jobs))
	for _, job := range jobs {
		currentPaths[job.path] = true
		progress.plan(job.path)
	}
	// A parent update can withdraw a missing foldout before it is cloned. Give
	// that originally planned checkout a terminal result rather than leaving
	// the progress total with work that can no longer run.
	for _, job := range initialJobs {
		if job.fixedPath && job.missing && !currentPaths[job.path] && !done(job.path) {
			progress.note(job.path, "foldout declaration removed by parent update")
			process(RepoStatus{Path: job.path, Target: job.target, Provider: job.provider, Org: job.org, Name: job.name, Missing: true})
		}
	}
	var missing []orgKey
	for _, org := range orgs {
		key := org.string()
		if _, ok := metadata.index[key]; !ok && metadata.errors[key] == nil {
			missing = append(missing, org)
		}
	}
	extra, failures := m.buildRepoIndex(missing, progress)
	for key, repos := range extra {
		metadata.index[key] = repos
	}
	for key, err := range failures {
		metadata.errors[key] = err
	}
	pool.Run(jobs, opts.Workers, func(job statusJob) struct{} {
		localProbe := &historyProbe{progress: progress}
		defer localProbe.close()
		scan(job, nil, localProbe)
		return struct{}{}
	})
	for _, t := range targets {
		key := orgKey{t.Provider, t.Org}.string()
		if t.Repo != "" || metadata.errors[key] == nil {
			continue
		}
		found := false
		for _, job := range jobs {
			if job.target == t.Name {
				found = true
				break
			}
		}
		if !found {
			process(RepoStatus{Path: t.Path, Target: t.Name, Provider: t.Provider, Org: t.Org, MetadataError: metadata.errors[key].Error()})
		}
	}
	return nil
}

func plannedSyncPaths(targets []config.Target, jobs []statusJob, metadata *scanMetadata, mode SyncMode) []string {
	var paths []string
	known := make(map[string]bool)
	for _, job := range jobs {
		known[job.path] = true
		paths = append(paths, job.path)
	}
	for _, target := range targets {
		if target.Repo != "" {
			continue
		}
		key := orgKey{target.Provider, target.Org}.string()
		if metadata.errors[key] != nil {
			found := false
			for _, job := range jobs {
				if job.target == target.Name {
					found = true
					break
				}
			}
			if !found {
				paths = append(paths, target.Path)
			}
			continue
		}
		if mode == SyncPush {
			continue
		}
		for _, repository := range metadata.index[key] {
			if !repository.Archived {
				dir := filepath.Join(target.Path, repository.Name)
				// Org discovery ignores symlinked directories and linked worktrees;
				// provisioning also leaves those existing checkouts untouched.
				if !known[dir] {
					if isGitRepo(dir) {
						continue
					}
					if info, err := os.Lstat(filepath.Join(dir, ".git")); err == nil && info.Mode().IsRegular() {
						continue
					}
				}
				paths = append(paths, dir)
			}
		}
	}
	return paths
}
