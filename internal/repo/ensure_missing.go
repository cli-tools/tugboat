package repo

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"gitea.swiftstrike.ai/swiftstrike/tugboat/internal/config"
	"gitea.swiftstrike.ai/swiftstrike/tugboat/internal/pool"
	"gitea.swiftstrike.ai/swiftstrike/tugboat/internal/remote"
)

type scanMetadata struct {
	index    map[string]map[string]remote.Repository
	errors   map[string]error
	readOnly bool
}

// Receiving syncs provision missing checkouts without printing an intermediate
// result. Their clone outcome is included in the single final sync line.
func (m *Manager) ensureMissingCheckouts(targets []config.Target, workers int, progress *progressReporter, metadata *scanMetadata, reserved map[string]bool, ready ...func(RepoStatus)) ([]RepoStatus, error) {
	seen := make(map[string]bool)
	for key := range metadata.index {
		seen[key] = true
	}
	for key := range metadata.errors {
		seen[key] = true
	}
	var extra []RepoStatus
	var jobs []statusJob
	queued := make(map[string]bool)
	emit := func(s RepoStatus) {
		if len(ready) > 0 && ready[0] != nil {
			ready[0](s)
		}
	}
	addExtra := func(s RepoStatus) { extra = append(extra, s); emit(s) }
	queue := func(t config.Target, org, name, dir string, r remote.Repository) {
		if reserved[dir] || queued[dir] {
			return
		}
		queued[dir] = true
		_, statErr := os.Lstat(dir)
		if statErr == nil {
			if isGitRepo(dir) {
				return
			}
			// Linked worktrees are existing checkouts, never clone destinations.
			if info, err := os.Lstat(filepath.Join(dir, ".git")); err == nil && info.Mode().IsRegular() {
				return
			}
		}
		if r.Archived {
			return
		}
		progress.plan(dir)
		if !strings.EqualFold(repositoryOwner(r, org), org) || r.Name != name {
			progress.note(dir, "configured repository moved to "+r.FullName+"; update its declaration")
			addExtra(RepoStatus{Path: dir, Target: t.Name, Provider: t.Provider, Org: org, Name: name, Missing: true})
			return
		}
		if t.Repo == "" {
			for _, pattern := range t.Exclude {
				if match, _ := path.Match(pattern, name); match {
					progress.note(dir, fmt.Sprintf("excluded by pattern %q", pattern))
					addExtra(RepoStatus{Path: dir, Target: t.Name, Provider: t.Provider, Org: org, Name: name, Missing: true})
					return
				}
			}
		}
		if statErr == nil {
			addExtra(RepoStatus{Path: dir, Target: t.Name, Provider: t.Provider, Org: org, Name: name, Missing: true, Error: "clone destination is occupied"})
			return
		}
		if !os.IsNotExist(statErr) {
			addExtra(RepoStatus{Path: dir, Target: t.Name, Provider: t.Provider, Org: org, Name: name, Missing: true, Error: statErr.Error()})
			return
		}
		jobs = append(jobs, statusJob{path: dir, target: t.Name, provider: t.Provider, org: org, name: name, token: m.config.Providers[t.Provider].Token})
	}
	run := func() {
		results := pool.Run(jobs, workers, func(job statusJob) RepoStatus {
			r := metadata.index[orgKey{job.provider, job.org}.string()][job.name]
			progress.detail("  [DETAIL] %s: cloning missing repository\n", job.path)
			err := os.MkdirAll(filepath.Dir(job.path), 0755)
			if err == nil {
				err = m.cloneVerified(job.provider, job.org, r, job.path)
			}
			if err != nil {
				s := RepoStatus{Path: job.path, Target: job.target, Provider: job.provider, Org: job.org, Name: job.name, Missing: true, Error: err.Error()}
				emit(s)
				return s
			}
			progress.note(job.path, "cloned missing repository")
			emit(RepoStatus{Path: job.path, Target: job.target, Provider: job.provider, Org: job.org, Name: job.name})
			return RepoStatus{}
		})
		for _, r := range results {
			if r.Error != "" {
				extra = append(extra, r)
			}
		}
		jobs = nil
	}
	for _, t := range targets {
		key := orgKey{t.Provider, t.Org}.string()
		if metadata.errors[key] != nil {
			continue
		}
		if t.Repo == "" {
			for _, r := range metadata.index[key] {
				queue(t, t.Org, r.Name, filepath.Join(t.Path, r.Name), r)
			}
		} else if r, ok := metadata.index[key][t.Repo]; ok {
			queue(t, t.Org, t.Repo, t.Path, r)
		}
	}
	run()
	for _, t := range targets {
		if t.Repo == "" || !isGitRepo(t.Path) || metadata.errors[orgKey{t.Provider, t.Org}.string()] != nil {
			continue
		}
		fc, err := loadFoldout(t.Path)
		if err != nil {
			return extra, err
		}
		if fc == nil {
			continue
		}
		if err := cleanFoldoutTargets(t.Path, fc.Repos); err != nil {
			return extra, err
		}
		for _, foldout := range fc.Repos {
			parts := strings.Split(foldout.Name, "/")
			org, name := parts[0], parts[1]
			key := orgKey{t.Provider, org}
			if !seen[key.string()] {
				index, failures := m.buildRepoIndex([]orgKey{key}, progress)
				for k, v := range index {
					metadata.index[k] = v
				}
				for k, v := range failures {
					metadata.errors[k] = v
				}
				seen[key.string()] = true
			}
			if metadata.errors[key.string()] != nil {
				continue
			}
			if r, ok := metadata.index[key.string()][name]; ok {
				queue(t, org, name, filepath.Join(t.Path, foldout.Target), r)
			}
		}
	}
	run()
	return extra, nil
}
