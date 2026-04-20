package repo

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gitea.swiftstrike.ai/swiftstrike/tugboat/internal/config"
	"gitea.swiftstrike.ai/swiftstrike/tugboat/internal/pool"
	"gitea.swiftstrike.ai/swiftstrike/tugboat/internal/remote"
)

type RepoTiming struct {
	Path      string
	Total     time.Duration
	Branch    time.Duration
	Fetch     time.Duration
	Status    time.Duration
	RevList   time.Duration
	MergeBase time.Duration
}

type RepoStatus struct {
	Path           string
	Target         string
	Provider       string
	Org            string
	Name           string
	Branch         string
	DefaultBranch  string
	Dirty          bool
	Ahead          int
	Behind         int
	CanFastForward bool
	UpstreamGone   bool
	Archived       bool
	Orphan         bool
	RemoteError    string
	Error          string
}

type foldoutRepo struct {
	Name   string `json:"name"`
	Target string `json:"target,omitempty"`
}

type foldoutConfig struct {
	Repos []foldoutRepo `json:"repos"`
}

type orgKey struct {
	provider string
	org      string
}

func (k orgKey) string() string { return k.provider + "|" + k.org }

type Manager struct {
	providers map[string]remote.Client
	config    *config.Config
}

func NewManager(providers map[string]remote.Client, cfg *config.Config) *Manager {
	return &Manager{providers: providers, config: cfg}
}

// ------------ selection helpers --------------

func (m *Manager) targetsFor(names []string) ([]config.Target, error) {
	if len(names) == 0 {
		return m.config.Targets, nil
	}
	nameSet := make(map[string]config.Target, len(m.config.Targets))
	for _, t := range m.config.Targets {
		nameSet[t.Name] = t
	}
	var res []config.Target
	var missing []string
	seen := make(map[string]bool)
	for _, n := range names {
		t, ok := nameSet[n]
		if !ok {
			missing = append(missing, n)
			continue
		}
		if seen[n] {
			continue
		}
		res = append(res, t)
		seen[n] = true
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("unknown targets: %s", strings.Join(missing, ", "))
	}
	return res, nil
}

// buildRepoIndex fetches remote repo metadata for the requested orgs (per provider).
// Key is provider|org, value is map[name]Repository.
func (m *Manager) buildRepoIndex(orgs []orgKey) (map[string]map[string]remote.Repository, error) {
	index := make(map[string]map[string]remote.Repository)
	for _, k := range orgs {
		client, ok := m.providers[k.provider]
		if !ok {
			return nil, fmt.Errorf("no client for provider %s", k.provider)
		}
		repos, err := client.ListOrgRepos(k.org)
		if err != nil {
			return nil, fmt.Errorf("listing repos for %s/%s: %w", k.provider, k.org, err)
		}
		m := make(map[string]remote.Repository, len(repos))
		for _, r := range repos {
			m[r.Name] = r
		}
		index[k.string()] = m
	}
	return index, nil
}

// ------------ foldout --------------

// loadFoldout loads .tugboat.json from path. Returns (nil, nil) if file doesn't exist.
func loadFoldout(path string) (*foldoutConfig, error) {
	data, err := os.ReadFile(filepath.Join(path, ".tugboat.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var fc foldoutConfig
	if err := json.Unmarshal(data, &fc); err != nil {
		return nil, fmt.Errorf("parsing .tugboat.json: %w", err)
	}
	for i := range fc.Repos {
		parts := strings.Split(fc.Repos[i].Name, "/")
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid repo name %q in .tugboat.json (expected org/repo)", fc.Repos[i].Name)
		}
		if fc.Repos[i].Target == "" {
			fc.Repos[i].Target = parts[len(parts)-1]
		}
	}
	return &fc, nil
}

func cleanFoldoutTargets(base string, repos []foldoutRepo) error {
	seen := make(map[string]bool)
	for _, r := range repos {
		if r.Target == "" {
			return fmt.Errorf("foldout target empty for %s", r.Name)
		}
		if strings.Contains(r.Target, "..") {
			return fmt.Errorf("foldout target %s must not contain ..", r.Target)
		}
		if seen[r.Target] {
			return fmt.Errorf("duplicate foldout target %s", r.Target)
		}
		seen[r.Target] = true
	}
	return nil
}

// ------------ clone --------------

type cloneJob struct {
	cloneURL string
	repoPath string
	repoName string
}

type cloneResult struct {
	repoName string
	status   string // cloned | exists | skipped | error | reset | rebased
	message  string
	err      error
}

type updateSkipError struct {
	reason string
}

func (e *updateSkipError) Error() string { return e.reason }

func (m *Manager) Clone(targetNames []string, excludeEmpty, includeArchived bool, workers int) error {
	targets, err := m.targetsFor(targetNames)
	if err != nil {
		return err
	}

	for _, t := range targets {
		if t.Repo == "" {
			if err := m.cloneOrg(t, excludeEmpty, includeArchived, workers); err != nil {
				return err
			}
		} else {
			if err := m.cloneRepoWithFoldout(t, excludeEmpty, includeArchived, workers); err != nil {
				return err
			}
		}
	}

	return nil
}

func (m *Manager) cloneOrg(t config.Target, excludeEmpty, includeArchived bool, workers int) error {
	client, ok := m.providers[t.Provider]
	if !ok {
		return fmt.Errorf("no client for provider %s", t.Provider)
	}

	repos, err := client.ListOrgRepos(t.Org)
	if err != nil {
		return fmt.Errorf("listing repos for %s: %w", t.Org, err)
	}

	// Build index for archived/orphan marking later (during status)

	if err := os.MkdirAll(t.Path, 0755); err != nil {
		return fmt.Errorf("creating directory %s: %w", t.Path, err)
	}

	sort.Slice(repos, func(i, j int) bool { return repos[i].Name < repos[j].Name })

	token := m.config.Providers[t.Provider].Token
	var jobs []cloneJob
	for _, r := range repos {
		if r.Empty && excludeEmpty {
			continue
		}
		if r.Archived && !includeArchived {
			continue
		}
		dest := filepath.Join(t.Path, r.Name)
		if isGitRepo(dest) {
			continue
		}
		jobs = append(jobs, cloneJob{
			cloneURL: pickCloneURL(&r, m.config.Providers[t.Provider].Options.Clone.Protocol),
			repoPath: dest,
			repoName: r.Name,
		})
	}

	if len(jobs) == 0 {
		fmt.Printf("Org %s: nothing to clone\n", t.Org)
		return nil
	}

	fmt.Printf("Org %s: cloning %d repositories...\n", t.Org, len(jobs))

	results := pool.Run(jobs, workers, func(job cloneJob) cloneResult {
		cmd := exec.Command("git", "clone", job.cloneURL, job.repoPath)
		cmd.Env = gitEnvWithAuth(token)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return cloneResult{repoName: job.repoName, status: "error", err: fmt.Errorf("%v: %s", err, output)}
		}
		return cloneResult{repoName: job.repoName, status: "cloned"}
	})

	var cloned, failed int
	for _, r := range results {
		if r.status == "cloned" {
			fmt.Printf("  [CLONED] %s\n", r.repoName)
			cloned++
		} else {
			fmt.Printf("  [ERROR]  %s: %v\n", r.repoName, r.err)
			failed++
		}
	}
	fmt.Printf("Org %s: clone complete (%d cloned, %d failed)\n", t.Org, cloned, failed)
	return nil
}

func (m *Manager) cloneRepoWithFoldout(t config.Target, excludeEmpty, includeArchived bool, workers int) error {
	client, ok := m.providers[t.Provider]
	if !ok {
		return fmt.Errorf("no client for provider %s", t.Provider)
	}
	repo, err := client.GetRepo(t.Org, t.Repo)
	if err != nil {
		return fmt.Errorf("fetching repo %s/%s: %w", t.Org, t.Repo, err)
	}
	if repo == nil {
		return fmt.Errorf("repo %s/%s not found (check that the repo exists and your token has access)", t.Org, t.Repo)
	}

	if repo.Empty && excludeEmpty {
		fmt.Printf("Skipping empty repo: %s/%s\n", t.Org, t.Repo)
		return nil
	}
	if repo.Archived && !includeArchived {
		fmt.Printf("Skipping archived repo: %s/%s\n", t.Org, t.Repo)
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(t.Path), 0755); err != nil {
		return fmt.Errorf("creating parent dir: %w", err)
	}

	token := m.config.Providers[t.Provider].Token
	if !isGitRepo(t.Path) {
		cloneURL := pickCloneURL(repo, m.config.Providers[t.Provider].Options.Clone.Protocol)
		fmt.Printf("Cloning %s/%s -> %s\n", t.Org, t.Repo, t.Path)
		cmd := exec.Command("git", "clone", cloneURL, t.Path)
		cmd.Env = gitEnvWithAuth(token)
		out, err := cmd.CombinedOutput()
		if err != nil {
			os.Stderr.Write(out)
			return err
		}
	} else {
		fmt.Printf("Exists: %s\n", t.Path)
	}

	// foldout
	fc, err := loadFoldout(t.Path)
	if err != nil {
		return err
	}
	if fc == nil {
		return nil // no foldout
	}
	if err := cleanFoldoutTargets(t.Path, fc.Repos); err != nil {
		return err
	}

	// Build clone jobs
	var jobs []cloneJob
	for _, fr := range fc.Repos {
		dest := filepath.Join(t.Path, fr.Target)
		if isGitRepo(dest) {
			continue
		}
		parts := strings.Split(fr.Name, "/")
		org := parts[0]
		repoName := parts[1]
		r, err := client.GetRepo(org, repoName)
		if err != nil {
			return fmt.Errorf("fetching foldout repo %s: %w", fr.Name, err)
		}
		if r == nil {
			fmt.Printf("  [MISS] %s not found\n", fr.Name)
			continue
		}
		if r.Empty && excludeEmpty {
			continue
		}
		if r.Archived && !includeArchived {
			continue
		}
		jobs = append(jobs, cloneJob{
			cloneURL: pickCloneURL(r, m.config.Providers[t.Provider].Options.Clone.Protocol),
			repoPath: dest,
			repoName: fr.Name,
		})
	}

	if len(jobs) == 0 {
		return nil
	}
	fmt.Printf("Foldout: cloning %d repos under %s\n", len(jobs), t.Path)
	results := pool.Run(jobs, workers, func(job cloneJob) cloneResult {
		cmd := exec.Command("git", "clone", job.cloneURL, job.repoPath)
		cmd.Env = gitEnvWithAuth(token)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return cloneResult{repoName: job.repoName, status: "error", err: fmt.Errorf("%v: %s", err, output)}
		}
		return cloneResult{repoName: job.repoName, status: "cloned"}
	})
	for _, r := range results {
		if r.status == "cloned" {
			fmt.Printf("  [CLONED] %s\n", r.repoName)
		} else {
			fmt.Printf("  [ERROR]  %s: %v\n", r.repoName, r.err)
		}
	}
	return nil
}

func pickCloneURL(r *remote.Repository, protocol string) string {
	switch protocol {
	case "ssh":
		return r.GetCloneURL(true)
	case "auto":
		if r.SSHURL != "" {
			return r.GetCloneURL(true)
		}
		return r.GetCloneURL(false)
	default: // https
		return r.GetCloneURL(false)
	}
}

// ------------ status / sync / pull / push --------------

type statusJob struct {
	path     string
	target   string
	name     string
	org      string
	provider string
	token    string
}

type statusResult struct {
	status RepoStatus
	timing RepoTiming
}

func (m *Manager) Status(targetNames []string, debug bool, workers int) error {
	targets, err := m.targetsFor(targetNames)
	if err != nil {
		return err
	}
	statuses, timings, err := m.getAllStatuses(targets, debug, workers)
	if err != nil {
		return err
	}

	var clean, dirty, ahead, behind, diverged, errored int
	for _, s := range statuses {
		if s.Error != "" {
			fmt.Printf("  [ERROR]    %s: %s\n", s.Path, s.Error)
			errored++
			continue
		}

		var flags []string
		if s.Dirty {
			flags = append(flags, "dirty")
			dirty++
		}
		if s.Ahead > 0 {
			flags = append(flags, fmt.Sprintf("%d ahead", s.Ahead))
			ahead++
		}
		if s.Behind > 0 {
			flags = append(flags, fmt.Sprintf("%d behind", s.Behind))
			behind++
			if !s.CanFastForward {
				flags = append(flags, "diverged")
				diverged++
			}
		}
		if s.RemoteError != "" {
			flags = append(flags, "remote: "+s.RemoteError)
		}
		if s.Archived {
			flags = append(flags, "archived")
		}
		if s.Orphan {
			flags = append(flags, "orphan")
		}
		if len(flags) > 0 {
			fmt.Printf("  %s (%s) [%s]\n", s.Path, s.Branch, strings.Join(flags, ", "))
		} else {
			fmt.Printf("  [CLEAN]  %s\n", s.Path)
			clean++
		}
	}

	fmt.Printf("\nSummary: %d clean, %d dirty, %d ahead, %d behind, %d diverged, %d errors\n",
		clean, dirty, ahead, behind, diverged, errored)

	if debug && len(timings) > 0 {
		totalTime := time.Duration(0)
		for _, t := range timings {
			totalTime += t.Total
		}
		fmt.Printf("\nDebug: %d repos, total time %v\n", len(timings), totalTime)
	}
	return nil
}

func (m *Manager) getAllStatuses(targets []config.Target, debug bool, workers int) ([]RepoStatus, []RepoTiming, error) {
	var jobs []statusJob
	var orgKeys []orgKey
	orgKeySet := make(map[string]bool)

	for _, t := range targets {
		tok := m.config.Providers[t.Provider].Token
		if t.Repo == "" {
			if _, err := os.Stat(t.Path); os.IsNotExist(err) {
				return nil, nil, fmt.Errorf("target %q path does not exist: %s", t.Name, t.Path)
			}
			entries, err := os.ReadDir(t.Path)
			if err != nil {
				continue
			}
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				repoPath := filepath.Join(t.Path, entry.Name())
				if !isGitRepo(repoPath) {
					continue
				}
				jobs = append(jobs, statusJob{path: repoPath, target: t.Name, name: entry.Name(), org: t.Org, provider: t.Provider, token: tok})
			}
			okey := orgKey{provider: t.Provider, org: t.Org}
			if !orgKeySet[okey.string()] {
				orgKeys = append(orgKeys, okey)
				orgKeySet[okey.string()] = true
			}
		} else {
			if _, err := os.Stat(t.Path); os.IsNotExist(err) {
				return nil, nil, fmt.Errorf("target %q path does not exist: %s", t.Name, t.Path)
			}
			if isGitRepo(t.Path) {
				jobs = append(jobs, statusJob{path: t.Path, target: t.Name, name: t.Repo, org: t.Org, provider: t.Provider, token: tok})
			}
			// foldout
			fc, err := loadFoldout(t.Path)
			if err != nil {
				return nil, nil, err
			}
			if fc != nil {
				for _, fr := range fc.Repos {
					dest := filepath.Join(t.Path, fr.Target)
					if isGitRepo(dest) {
						parts := strings.Split(fr.Name, "/")
						repoName := parts[len(parts)-1]
						frOrg := t.Org
						if len(parts) == 2 {
							frOrg = parts[0]
						}
						jobs = append(jobs, statusJob{path: dest, target: t.Name, name: repoName, org: frOrg, provider: t.Provider, token: tok})
						okey := orgKey{provider: t.Provider, org: frOrg}
						if !orgKeySet[okey.string()] {
							orgKeys = append(orgKeys, okey)
							orgKeySet[okey.string()] = true
						}
					}
				}
			}
			// Collect orgKey for single-repo targets too (for orphan/archived detection)
			okey := orgKey{provider: t.Provider, org: t.Org}
			if !orgKeySet[okey.string()] {
				orgKeys = append(orgKeys, okey)
				orgKeySet[okey.string()] = true
			}
		}
	}

	if len(jobs) == 0 {
		return nil, nil, nil
	}

	results := pool.Run(jobs, workers, func(job statusJob) statusResult {
		var timing RepoTiming
		status := getRepoStatus(job.path, job.target, job.org, job.name, job.provider, job.token, &timing)
		return statusResult{status: status, timing: timing}
	})

	statuses := make([]RepoStatus, len(results))
	timings := make([]RepoTiming, len(results))
	for i, r := range results {
		statuses[i] = r.status
		timings[i] = r.timing
	}

	// mark archived/orphan
	if len(orgKeys) > 0 {
		if index, err := m.buildRepoIndex(orgKeys); err == nil {
			markRemoteState(statuses, index)
		}
	}

	sort.Slice(statuses, func(i, j int) bool {
		if statuses[i].Target == statuses[j].Target {
			return statuses[i].Name < statuses[j].Name
		}
		return statuses[i].Target < statuses[j].Target
	})

	if debug {
		sort.Slice(timings, func(i, j int) bool {
			return timings[i].Total > timings[j].Total
		})
	}

	return statuses, timings, nil
}

// ------------ auth helpers --------------

// gitEnvNoPrompt returns the current process environment with
// GIT_TERMINAL_PROMPT=0 to prevent interactive credential prompts.
func gitEnvNoPrompt() []string {
	return append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
}

// gitEnvWithAuth returns an environment that disables prompts and, when token
// is non-empty, injects an ephemeral credential helper via GIT_CONFIG env vars
// so that HTTPS git operations can authenticate without persisting credentials
// to disk.  SSH operations are unaffected (they use ~/.ssh and ssh-agent).
func gitEnvWithAuth(token string) []string {
	env := gitEnvNoPrompt()
	if token == "" {
		return env
	}
	// Use GIT_CONFIG_COUNT/KEY/VALUE to inject an inline credential helper
	// that echoes the token.  This avoids mutating .git/config.
	helper := fmt.Sprintf("!f() { echo username=x-access-token; echo password=%s; }; f", token)
	env = append(env,
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=credential.helper",
		"GIT_CONFIG_VALUE_0="+helper,
	)
	return env
}

// ------------ git helpers --------------

func isGitRepo(path string) bool {
	gitDir := filepath.Join(path, ".git")
	info, err := os.Stat(gitDir)
	return err == nil && info.IsDir()
}

func getRepoStatus(path, target, org, name, provider, token string, timing *RepoTiming) RepoStatus {
	totalStart := time.Now()
	status := RepoStatus{
		Path:     path,
		Target:   target,
		Provider: provider,
		Org:      org,
		Name:     name,
	}

	// Get current branch
	branchStart := time.Now()
	branch, err := gitOutput(path, "rev-parse", "--abbrev-ref", "HEAD")
	if timing != nil {
		timing.Branch = time.Since(branchStart)
	}
	if err != nil {
		status.Error = fmt.Sprintf("getting branch: %v", err)
		return status
	}
	status.Branch = strings.TrimSpace(branch)

	// Fetch from remote
	fetchStart := time.Now()
	if fetchErr := gitFetchWithStderr(path, token); fetchErr != "" {
		status.RemoteError = fetchErr
	}
	if timing != nil {
		timing.Fetch = time.Since(fetchStart)
	}

	// Check for uncommitted changes
	statusStart := time.Now()
	dirtyOutput, err := gitOutput(path, "status", "--porcelain")
	if timing != nil {
		timing.Status = time.Since(statusStart)
	}
	if err != nil {
		status.Error = fmt.Sprintf("checking status: %v", err)
		return status
	}
	status.Dirty = strings.TrimSpace(dirtyOutput) != ""

	// Get ahead/behind counts
	revListStart := time.Now()
	upstream := fmt.Sprintf("origin/%s", status.Branch)
	revList, err := gitOutput(path, "rev-list", "--left-right", "--count", fmt.Sprintf("%s...%s", status.Branch, upstream))
	if timing != nil {
		timing.RevList = time.Since(revListStart)
	}
	if err == nil {
		parts := strings.Fields(strings.TrimSpace(revList))
		if len(parts) == 2 {
			fmt.Sscanf(parts[0], "%d", &status.Ahead)
			fmt.Sscanf(parts[1], "%d", &status.Behind)
		}
	} else if status.RemoteError == "" {
		// rev-list failed after a successful fetch — the upstream ref is gone.
		status.UpstreamGone = true
	}

	mergeBaseStart := time.Now()
	if status.Behind > 0 {
		err := gitRun(path, "merge-base", "--is-ancestor", status.Branch, upstream)
		status.CanFastForward = (err == nil) || (status.Ahead == 0)
	} else {
		status.CanFastForward = true
	}
	if timing != nil {
		timing.MergeBase = time.Since(mergeBaseStart)
		timing.Total = time.Since(totalStart)
		timing.Path = path
	}

	return status
}

func gitOutput(repoPath string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = repoPath
	cmd.Env = gitEnvNoPrompt()
	output, err := cmd.Output()
	return string(output), err
}

func gitRun(repoPath string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = repoPath
	cmd.Env = gitEnvNoPrompt()
	return cmd.Run()
}

func gitFetchWithStderr(repoPath, token string) string {
	cmd := exec.Command("git", "fetch", "--quiet")
	cmd.Dir = repoPath
	cmd.Env = gitEnvWithAuth(token)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		output := strings.TrimSpace(stderr.String())
		if idx := strings.Index(output, "\n"); idx > 0 {
			output = output[:idx]
		}
		return output
	}
	return ""
}

// Pull/Push helpers used by sync-like commands
func gitPull(repoPath string, ffOnly bool, token string) error {
	args := []string{"pull"}
	if ffOnly {
		args = append(args, "--ff-only")
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = repoPath
	cmd.Env = gitEnvWithAuth(token)
	out, err := cmd.CombinedOutput()
	if err != nil {
		os.Stderr.Write(out)
	}
	return err
}

func gitPullRebase(repoPath string, token string) error {
	cmd := exec.Command("git", "pull", "--rebase=merges")
	cmd.Dir = repoPath
	cmd.Env = gitEnvWithAuth(token)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Abort the rebase so the repo is not left in a broken mid-rebase state.
		abort := exec.Command("git", "rebase", "--abort")
		abort.Dir = repoPath
		abort.Env = gitEnvNoPrompt()
		abort.Run() // best-effort
		os.Stderr.Write(out)
	}
	return err
}

// gitPullWithFallback tries a normal pull (ff-only when requested) and, if
// that fails because the branch has diverged, falls back to a rebase pull.
// Returns (true, nil) when the fallback rebase succeeded.  If the rebase
// itself fails (e.g. conflicts) it is aborted so the repo stays clean.
func gitPullWithFallback(repoPath string, ffOnly bool, token string) (rebased bool, err error) {
	args := []string{"pull"}
	if ffOnly {
		args = append(args, "--ff-only")
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = repoPath
	cmd.Env = gitEnvWithAuth(token)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return false, nil
	}
	// Only fall back to rebase when ff-only was requested and the failure is
	// specifically because branches have diverged.  Other failures (auth,
	// network, missing remote, etc.) must not trigger a rebase attempt.
	if !ffOnly || !strings.Contains(string(out), "Not possible to fast-forward") {
		os.Stderr.Write(out)
		return false, err
	}
	// Fallback: rebase with merge preservation.
	cmd2 := exec.Command("git", "pull", "--rebase=merges")
	cmd2.Dir = repoPath
	cmd2.Env = gitEnvWithAuth(token)
	out2, err2 := cmd2.CombinedOutput()
	if err2 != nil {
		// Abort the rebase so the repo is not left in a broken mid-rebase state.
		abort := exec.Command("git", "rebase", "--abort")
		abort.Dir = repoPath
		abort.Env = gitEnvNoPrompt()
		abort.Run() // best-effort
		os.Stderr.Write(out2)
		return false, err2
	}
	return true, nil
}

func gitPush(repoPath, token string) error {
	cmd := exec.Command("git", "push")
	cmd.Dir = repoPath
	cmd.Env = gitEnvWithAuth(token)
	out, err := cmd.CombinedOutput()
	if err != nil {
		os.Stderr.Write(out)
	}
	return err
}

// hasUpstreamRef fetches from origin and checks whether the current branch
// has a corresponding remote-tracking ref. Returns (exists, branchName, error).
// Returns an error if fetch fails, so callers can distinguish "verified missing"
// from "could not verify".
func hasUpstreamRef(repoPath, token string) (bool, string, error) {
	branch, err := gitOutput(repoPath, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return false, "", fmt.Errorf("getting branch: %w", err)
	}
	branch = strings.TrimSpace(branch)
	// Fetch with auth so HTTPS repos can authenticate.
	cmd := exec.Command("git", "fetch", "--quiet")
	cmd.Dir = repoPath
	cmd.Env = gitEnvWithAuth(token)
	if err := cmd.Run(); err != nil {
		return false, branch, fmt.Errorf("fetch failed: %w", err)
	}
	upstream := fmt.Sprintf("origin/%s", branch)
	err = gitRun(repoPath, "rev-parse", "--verify", "--quiet", upstream)
	return err == nil, branch, nil
}

func defaultBranchFromOriginHead(repoPath string) (string, error) {
	ref, err := gitOutput(repoPath, "symbolic-ref", "refs/remotes/origin/HEAD")
	if err != nil {
		return "", fmt.Errorf("cannot determine default branch (origin/HEAD not set)")
	}
	defaultBranch := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ref), "refs/remotes/origin/"))
	if defaultBranch == "" {
		return "", fmt.Errorf("empty default branch from origin/HEAD")
	}
	return defaultBranch, nil
}

func resolveDefaultBranch(repoPath, remoteDefault string) (string, error) {
	if strings.TrimSpace(remoteDefault) != "" {
		return strings.TrimSpace(remoteDefault), nil
	}
	return defaultBranchFromOriginHead(repoPath)
}

func localBranchExists(repoPath, branch string) bool {
	return gitRun(repoPath, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch) == nil
}

func remoteTrackingRefExists(repoPath, branch string) bool {
	return gitRun(repoPath, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+branch) == nil
}

func branchHasCommitsOutsideDefaultBranch(repoPath, branch, defaultBranch string) (bool, error) {
	baseRef := "origin/" + defaultBranch
	if !remoteTrackingRefExists(repoPath, defaultBranch) {
		if localBranchExists(repoPath, defaultBranch) {
			baseRef = defaultBranch
		} else {
			return false, fmt.Errorf("default branch %q is not available locally or on origin", defaultBranch)
		}
	}
	revList, err := gitOutput(repoPath, "rev-list", fmt.Sprintf("%s..%s", baseRef, branch))
	if err != nil {
		return false, fmt.Errorf("checking whether %s is contained in %s: %w", branch, defaultBranch, err)
	}
	return strings.TrimSpace(revList) != "", nil
}

func ensureLocalBranch(repoPath, branch string) error {
	if localBranchExists(repoPath, branch) {
		return nil
	}
	if !remoteTrackingRefExists(repoPath, branch) {
		return fmt.Errorf("default branch %q is not available on origin", branch)
	}
	cmd := exec.Command("git", "switch", "-c", branch, "--track", "origin/"+branch)
	cmd.Dir = repoPath
	cmd.Env = gitEnvNoPrompt()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("creating local %s from origin/%s: %v: %s", branch, branch, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// switchToDefaultBranch moves a repo onto its default branch when it is safe to
// abandon the current branch context. Dirty repos and branches with local-only
// commits are refused with updateSkipError so callers can warn and continue.
func switchToDefaultBranch(repoPath, branch, defaultBranch string) error {
	if defaultBranch == "" {
		return fmt.Errorf("default branch is empty")
	}
	if branch == defaultBranch {
		return nil
	}

	dirtyOutput, err := gitOutput(repoPath, "status", "--porcelain")
	if err != nil {
		return fmt.Errorf("checking status: %w", err)
	}
	if strings.TrimSpace(dirtyOutput) != "" {
		return &updateSkipError{reason: fmt.Sprintf("on %s, dirty; not updating non-default branch", branch)}
	}

	if remoteTrackingRefExists(repoPath, branch) {
		localOnly, err := gitOutput(repoPath, "rev-list", fmt.Sprintf("origin/%s..%s", branch, branch))
		if err != nil {
			return fmt.Errorf("checking local-only commits on %s: %w", branch, err)
		}
		if strings.TrimSpace(localOnly) != "" {
			return &updateSkipError{reason: fmt.Sprintf("on %s, has local-only commits; not updating non-default branch", branch)}
		}
	} else {
		hasExtraCommits, err := branchHasCommitsOutsideDefaultBranch(repoPath, branch, defaultBranch)
		if err != nil {
			return err
		}
		if hasExtraCommits {
			return &updateSkipError{reason: fmt.Sprintf("on %s, commits are not on %s; not switching", branch, defaultBranch)}
		}
	}

	if err := ensureLocalBranch(repoPath, defaultBranch); err != nil {
		return err
	}
	if err := gitRun(repoPath, "switch", defaultBranch); err != nil {
		return fmt.Errorf("git switch %s: %w", defaultBranch, err)
	}
	return nil
}

// markRemoteState annotates archived/orphan based on remote index.
func markRemoteState(statuses []RepoStatus, index map[string]map[string]remote.Repository) {
	for i := range statuses {
		key := orgKey{provider: statuses[i].Provider, org: statuses[i].Org}.string()
		repos, ok := index[key]
		if !ok {
			statuses[i].Orphan = true
			continue
		}
		if r, ok := repos[statuses[i].Name]; ok {
			statuses[i].Archived = r.Archived
			statuses[i].DefaultBranch = r.DefaultBranch
		} else {
			statuses[i].Orphan = true
		}
	}
}

func (m *Manager) prepareRepoForDefaultBranch(s RepoStatus, token string) (RepoStatus, bool, error) {
	defaultBranch := strings.TrimSpace(s.DefaultBranch)
	if defaultBranch != "" && s.Branch == defaultBranch {
		return s, false, nil
	}
	if defaultBranch == "" {
		resolvedDefault, err := resolveDefaultBranch(s.Path, s.DefaultBranch)
		if err != nil {
			// Fall back to the currently checked out branch when the default
			// branch cannot be determined at all.
			return s, false, nil
		}
		defaultBranch = resolvedDefault
		s.DefaultBranch = defaultBranch
		if s.Branch == defaultBranch {
			return s, false, nil
		}
	}

	if s.Dirty {
		return s, false, &updateSkipError{reason: fmt.Sprintf("on %s, dirty; not updating non-default branch", s.Branch)}
	}
	if s.Ahead > 0 {
		return s, false, &updateSkipError{reason: fmt.Sprintf("on %s, %d ahead; not updating non-default branch", s.Branch, s.Ahead)}
	}

	if err := switchToDefaultBranch(s.Path, s.Branch, defaultBranch); err != nil {
		return s, false, err
	}

	refreshed := getRepoStatus(s.Path, s.Target, s.Org, s.Name, s.Provider, token, nil)
	refreshed.DefaultBranch = defaultBranch
	refreshed.Archived = s.Archived
	refreshed.Orphan = s.Orphan
	return refreshed, true, nil
}

// TODO: implement sync/pull/push/list using the new target model.
func (m *Manager) Pull(targetNames []string, workers int) error {
	targets, err := m.targetsFor(targetNames)
	if err != nil {
		return err
	}

	var existingTargets []config.Target
	for _, t := range targets {
		if _, err := os.Stat(t.Path); err == nil {
			existingTargets = append(existingTargets, t)
		}
	}

	statuses, _, err := m.getAllStatuses(existingTargets, false, workers)
	if err != nil {
		return err
	}
	if len(statuses) == 0 {
		fmt.Println("Pull: no repositories found.")
		return nil
	}

	optMap := make(map[string]config.ProviderOptions)
	tokenMap := make(map[string]string)
	for _, t := range targets {
		optMap[t.Name] = m.config.Providers[t.Provider].Options
		tokenMap[t.Name] = m.config.Providers[t.Provider].Token
	}

	var pulled, skipped, failed int
	for _, s := range statuses {
		opts := optMap[s.Target]
		tok := tokenMap[s.Target]

		if s.Error != "" {
			fmt.Printf("  [ERROR] %s: %s\n", s.Path, s.Error)
			failed++
			continue
		}

		prepared, switched, err := m.prepareRepoForDefaultBranch(s, tok)
		if err != nil {
			var skipErr *updateSkipError
			if errors.As(err, &skipErr) {
				fmt.Printf("  [SKIP]  %s: %s\n", s.Path, skipErr.reason)
				skipped++
				continue
			}
			fmt.Printf("  [ERROR] %s: %v\n", s.Path, err)
			failed++
			continue
		}
		if switched {
			fmt.Printf("  [SWITCH] %s: %s -> %s\n", s.Path, s.Branch, prepared.DefaultBranch)
		}
		if prepared.Error != "" {
			fmt.Printf("  [ERROR] %s: %s\n", prepared.Path, prepared.Error)
			failed++
			continue
		}
		if prepared.Dirty {
			fmt.Printf("  [SKIP]  %s: dirty\n", prepared.Path)
			skipped++
			continue
		}

		rebased, err := gitPullWithFallback(prepared.Path, opts.Sync.GetFFOnly(), tok)
		if err != nil {
			fmt.Printf("  [ERROR] %s: %v\n", prepared.Path, err)
			failed++
			continue
		}
		if rebased {
			fmt.Printf("  [REBASE] %s\n", prepared.Path)
		} else {
			fmt.Printf("  [PULL]  %s\n", prepared.Path)
		}
		pulled++
	}

	fmt.Printf("Pull complete: %d pulled, %d skipped, %d failed\n", pulled, skipped, failed)
	return nil
}

func (m *Manager) Push(targetNames []string, workers int) error {
	targets, err := m.targetsFor(targetNames)
	if err != nil {
		return err
	}

	statuses, _, err := m.getAllStatuses(targets, false, workers)
	if err != nil {
		return err
	}

	// Build target -> token map for push authentication.
	tokenMap := make(map[string]string)
	for _, t := range targets {
		tokenMap[t.Name] = m.config.Providers[t.Provider].Token
	}

	var pushed, skipped, failed int
	for _, s := range statuses {
		if s.Error != "" {
			fmt.Printf("  [ERROR] %s: %s\n", s.Path, s.Error)
			failed++
			continue
		}
		if s.Behind > 0 {
			fmt.Printf("  [SKIP]  %s: behind remote, pull first\n", s.Path)
			skipped++
			continue
		}
		if s.Ahead == 0 {
			continue
		}
		if err := gitPush(s.Path, tokenMap[s.Target]); err != nil {
			fmt.Printf("  [ERROR] %s: %v\n", s.Path, err)
			failed++
		} else {
			fmt.Printf("  [PUSH]  %s: %d commits\n", s.Path, s.Ahead)
			pushed++
		}
	}
	fmt.Printf("Push complete: %d pushed, %d skipped, %d failed\n", pushed, skipped, failed)
	return nil
}

func (m *Manager) Sync(targetNames []string, workers int) error {
	targets, err := m.targetsFor(targetNames)
	if err != nil {
		return err
	}
	statuses, _, err := m.getAllStatuses(targets, false, workers)
	if err != nil {
		return err
	}

	// map target -> options and tokens
	optMap := make(map[string]config.ProviderOptions)
	tokenMap := make(map[string]string)
	for _, t := range targets {
		optMap[t.Name] = m.config.Providers[t.Provider].Options
		tokenMap[t.Name] = m.config.Providers[t.Provider].Token
	}

	var synced, skipped, failed int
	for _, s := range statuses {
		opts := optMap[s.Target]
		tok := tokenMap[s.Target]

		if s.Error != "" {
			fmt.Printf("  [ERROR] %s: %s\n", s.Path, s.Error)
			failed++
			continue
		}
		prepared, switched, err := m.prepareRepoForDefaultBranch(s, tok)
		if err != nil {
			var skipErr *updateSkipError
			if errors.As(err, &skipErr) {
				fmt.Printf("  [SKIP]  %s: %s\n", s.Path, skipErr.reason)
				skipped++
				continue
			}
			fmt.Printf("  [ERROR] %s: %v\n", s.Path, err)
			failed++
			continue
		}
		if switched {
			fmt.Printf("  [SWITCH] %s: %s -> %s\n", s.Path, s.Branch, prepared.DefaultBranch)
		}
		if prepared.Error != "" {
			fmt.Printf("  [ERROR] %s: %s\n", prepared.Path, prepared.Error)
			failed++
			continue
		}
		if prepared.Dirty {
			fmt.Printf("  [SKIP]  %s: dirty\n", prepared.Path)
			skipped++
			continue
		}

		if prepared.Behind > 0 {
			if !prepared.CanFastForward && opts.Sync.GetFFOnly() {
				// Diverged: ff-only would fail, go straight to rebase.
				fmt.Printf("  [REBASE] %s: %d behind, %d ahead (diverged)\n", prepared.Path, prepared.Behind, prepared.Ahead)
				if err := gitPullRebase(prepared.Path, tok); err != nil {
					fmt.Printf("    error: %v\n", err)
					failed++
					continue
				}
			} else {
				fmt.Printf("  [PULL]  %s: %d behind\n", prepared.Path, prepared.Behind)
				if err := gitPull(prepared.Path, opts.Sync.GetFFOnly(), tok); err != nil {
					fmt.Printf("    error: %v\n", err)
					failed++
					continue
				}
			}
		}
		if prepared.Ahead > 0 {
			fmt.Printf("  [PUSH]  %s: %d ahead\n", prepared.Path, prepared.Ahead)
			if err := gitPush(prepared.Path, tok); err != nil {
				fmt.Printf("    error: %v\n", err)
				failed++
				continue
			}
		}
		synced++
	}
	fmt.Printf("Sync complete: %d synced, %d skipped, %d failed\n", synced, skipped, failed)
	return nil
}

func (m *Manager) List(targetNames []string, includeArchived bool, workers int) error {
	targets, err := m.targetsFor(targetNames)
	if err != nil {
		return err
	}

	for _, t := range targets {
		fmt.Printf("Target: %s (%s/%s) path=%s\n", t.Name, t.Provider, t.Org, t.Path)
		if t.Repo == "" {
			client, ok := m.providers[t.Provider]
			if !ok {
				fmt.Printf("  [ERROR] no client for provider %s\n\n", t.Provider)
				continue
			}

			remoteMap := make(map[string]remote.Repository)
			if repos, err := client.ListOrgRepos(t.Org); err == nil {
				for _, r := range repos {
					remoteMap[r.Name] = r
				}
			} else {
				fmt.Printf("  [ERROR] listing org: %v\n", err)
			}

			local := make(map[string]bool)
			entries, _ := os.ReadDir(t.Path)
			for _, e := range entries {
				if e.IsDir() && isGitRepo(filepath.Join(t.Path, e.Name())) {
					local[e.Name()] = true
				}
			}

			names := make([]string, 0, len(remoteMap))
			for n := range remoteMap {
				names = append(names, n)
			}
			sort.Strings(names)

			for _, n := range names {
				r := remoteMap[n]
				// Skip archived repos unless --include-archived is set
				if r.Archived && !includeArchived {
					continue
				}
				mark := "[ ]"
				if local[n] {
					mark = "[x]"
				}
				flags := []string{}
				if r.Archived {
					flags = append(flags, "archived")
				}
				fmt.Printf("  %s %s", mark, n)
				if len(flags) > 0 {
					fmt.Printf(" (%s)", strings.Join(flags, ", "))
				}
				fmt.Println()
			}

			// local only -> orphan
			var orphans []string
			for n := range local {
				if _, ok := remoteMap[n]; !ok {
					orphans = append(orphans, n)
				}
			}
			sort.Strings(orphans)
			for _, n := range orphans {
				fmt.Printf("  [x] %s (orphan)\n", n)
			}

		} else {
			mark := "[ ]"
			if isGitRepo(t.Path) {
				mark = "[x]"
			}
			fmt.Printf("  %s %s\n", mark, t.Repo)
			fc, err := loadFoldout(t.Path)
			if err != nil {
				return err
			}
			if fc != nil {
				for _, fr := range fc.Repos {
					dest := filepath.Join(t.Path, fr.Target)
					m := "[ ]"
					if isGitRepo(dest) {
						m = "[x]"
					}
					fmt.Printf("  %s %s -> %s\n", m, fr.Name, fr.Target)
				}
			}
		}
		fmt.Println()
	}
	return nil
}
