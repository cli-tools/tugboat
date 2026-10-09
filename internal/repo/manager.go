package repo

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"text/tabwriter"
	"time"

	"gitea.swiftstrike.ai/swiftstrike/tugboat/internal/cache"
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
	RepositoryID   int64
	RemoteName     string
	ReplacementID  int64
	IdentityIssue  string
	ReconcileError bool
	Transferred    bool
	Removed        bool
	identity       *checkoutIdentity
	repository     *remote.Repository
	gitDirInfo     os.FileInfo
	Path           string
	Target         string
	Provider       string
	Org            string
	Name           string
	Branch         string
	DefaultBranch  string
	Unborn         bool
	RemoteEmpty    bool
	LocalCommits   int
	Dirty          bool
	Ahead          int
	Behind         int
	CanFastForward bool
	UpstreamGone   bool
	Archived       bool
	Orphan         bool
	Missing        bool
	RemoteError    string
	MetadataError  string
	Error          string
}

type StatusOptions struct {
	Debug   bool
	ShowAll bool
	Workers int
}

type SyncMode int

const (
	SyncBoth SyncMode = iota
	SyncPull
	SyncPush
	SyncCloneOnly
)

type SyncOptions struct {
	Mode            SyncMode
	RemoveArchived  bool
	ExcludeEmpty    bool
	IncludeArchived bool
	Workers         int
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
	// Verbose includes diagnostic activity alongside numbered progress.
	Verbose bool
	// Cache holds disposable local discovery data, never repository state.
	Cache        *cache.Store
	RefreshCache bool
	providers    map[string]remote.Client
	config       *config.Config
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
// Keys are provider|org. Errors are retained per organization so callers can
// fail closed without losing metadata that was fetched successfully elsewhere.
func (m *Manager) buildRepoIndex(orgs []orgKey, progress *progressReporter) (map[string]map[string]remote.Repository, map[string]error) {
	index := make(map[string]map[string]remote.Repository)
	errorsByOrg := make(map[string]error)
	for _, k := range orgs {
		key := k.string()
		progress.detail("Loading repository metadata for %s/%s...\n", k.provider, k.org)
		client, ok := m.providers[k.provider]
		if !ok {
			errorsByOrg[key] = fmt.Errorf("no client for provider %s", k.provider)
			progress.detail("  Metadata failed for %s/%s: %v\n", k.provider, k.org, errorsByOrg[key])
			continue
		}
		repos, err := client.ListOrgRepos(k.org)
		if err != nil {
			errorsByOrg[key] = fmt.Errorf("listing repos for %s/%s: %w", k.provider, k.org, err)
			progress.detail("  Metadata failed for %s/%s: %v\n", k.provider, k.org, errorsByOrg[key])
			continue
		}
		reposByName := make(map[string]remote.Repository, len(repos))
		for _, r := range repos {
			reposByName[r.Name] = r
		}
		index[key] = reposByName
		progress.detail("  Metadata loaded for %s/%s\n", k.provider, k.org)
	}
	return index, errorsByOrg
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
	provider   string
	org        string
	repository remote.Repository
	repoPath   string
	repoName   string
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
	return m.Sync(targetNames, SyncOptions{Mode: SyncCloneOnly, ExcludeEmpty: excludeEmpty, IncludeArchived: includeArchived, Workers: workers})
}

func (m *Manager) cloneOnly(targetNames []string, excludeEmpty, includeArchived bool, workers int) error {
	targets, err := m.targetsFor(targetNames)
	if err != nil {
		return err
	}
	// Validate every selected target before any provider requests or cloning.
	for _, t := range targets {
		if err := t.ValidateExclusions(); err != nil {
			return err
		}
	}

	probe := &historyProbe{progress: &progressReporter{out: os.Stdout, verbose: m.Verbose}}
	defer probe.close()
	for _, t := range targets {
		if t.Repo == "" {
			if err := m.cloneOrg(t, excludeEmpty, includeArchived, workers, probe); err != nil {
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

func (m *Manager) cloneOrg(t config.Target, excludeEmpty, includeArchived bool, workers int, probe *historyProbe) error {
	client, ok := m.providers[t.Provider]
	if !ok {
		return fmt.Errorf("no client for provider %s", t.Provider)
	}

	repos, err := client.ListOrgRepos(t.Org)
	if err != nil {
		return fmt.Errorf("listing repos for %s: %w", t.Org, err)
	}

	remoteMap := make(map[string]remote.Repository)
	for _, r := range repos {
		remoteMap[r.Name] = r
	}
	fmt.Printf("Org %s: checking repository identities and renames...\n", t.Org)
	for dir, err := range m.reconcileOrg(t, remoteMap, probe, excludeEmpty, includeArchived) {
		return fmt.Errorf("reconciling %s: %w", dir, err)
	}
	var reconciledPaths []string
	for path := range probe.progress.notes {
		if filepath.Dir(path) == t.Path {
			reconciledPaths = append(reconciledPaths, path)
		}
	}
	sort.Strings(reconciledPaths)
	for _, path := range reconciledPaths {
		fmt.Printf("  [RECONCILED] %s: %s\n", path, strings.Join(probe.progress.notes[path], "; "))
	}

	if err := os.MkdirAll(t.Path, 0755); err != nil {
		return fmt.Errorf("creating directory %s: %w", t.Path, err)
	}

	sort.Slice(repos, func(i, j int) bool { return repos[i].Name < repos[j].Name })

	var jobs []cloneJob
repositories:
	for _, r := range repos {
		for _, pattern := range t.Exclude {
			matched, err := path.Match(pattern, r.Name)
			if err != nil {
				return fmt.Errorf("target %q: matching exclude pattern %q: %w", t.Name, pattern, err)
			}
			if matched {
				fmt.Printf("  [SKIP] %s/%s: excluded by pattern %q\n", t.Org, r.Name, pattern)
				continue repositories
			}
		}
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
			provider: t.Provider, org: t.Org, repository: r,
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
		if err := m.cloneVerified(job.provider, job.org, job.repository, job.repoPath); err != nil {
			return cloneResult{repoName: job.repoName, status: "error", err: err}
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
	if failed > 0 {
		return fmt.Errorf("%d clones failed", failed)
	}
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

	if isGitRepo(t.Path) {
		if err := m.verifyExistingClone(t.Provider, t.Org, t.Repo, t.Path); err != nil {
			var skip *updateSkipError
			if errors.As(err, &skip) {
				fmt.Printf("  [SKIP] %s: %s; explicit targets are not relocated automatically\n", t.Path, skip.reason)
				return nil
			}
			return err
		}
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

	if !isGitRepo(t.Path) {
		fmt.Printf("Cloning %s/%s -> %s\n", t.Org, t.Repo, t.Path)
		if err := m.cloneVerified(t.Provider, t.Org, *repo, t.Path); err != nil {
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
		if isGitRepo(dest) {
			if err := m.verifyExistingClone(t.Provider, org, repoName, dest); err != nil {
				var skip *updateSkipError
				if errors.As(err, &skip) {
					fmt.Printf("  [SKIP] %s: %s; foldouts are not relocated automatically\n", dest, skip.reason)
					continue
				}
				return err
			}
			continue
		}
		if r.Empty && excludeEmpty {
			continue
		}
		if r.Archived && !includeArchived {
			continue
		}
		jobs = append(jobs, cloneJob{
			provider: t.Provider, org: org, repository: *r,
			repoPath: dest,
			repoName: fr.Name,
		})
	}

	if len(jobs) == 0 {
		return nil
	}
	fmt.Printf("Foldout: cloning %d repos under %s\n", len(jobs), t.Path)
	results := pool.Run(jobs, workers, func(job cloneJob) cloneResult {
		if err := m.cloneVerified(job.provider, job.org, job.repository, job.repoPath); err != nil {
			return cloneResult{repoName: job.repoName, status: "error", err: err}
		}
		return cloneResult{repoName: job.repoName, status: "cloned"}
	})
	var failed int
	for _, r := range results {
		if r.status == "cloned" {
			fmt.Printf("  [CLONED] %s\n", r.repoName)
		} else {
			fmt.Printf("  [ERROR] %s: %v\n", r.repoName, r.err)
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d foldout clones failed", failed)
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
	path      string
	target    string
	name      string
	org       string
	provider  string
	token     string
	missing   bool
	fixedPath bool
}

type statusResult struct {
	status RepoStatus
	timing RepoTiming
}

type statusGroup int

const (
	groupArchived statusGroup = iota
	groupAttention
	groupMissing
	groupEmpty
	groupClean
)

func (m *Manager) Status(targetNames []string, opts StatusOptions) error {
	targets, err := m.targetsFor(targetNames)
	if err != nil {
		return err
	}
	statuses, timings, err := m.getAllStatuses(targets, opts.Debug, opts.Workers, nil)
	if err != nil {
		return err
	}

	byTarget := make(map[string][]RepoStatus, len(targets))
	var clean, empty, dirty, ahead, behind, diverged, archived, orphan, missing, errored int
	for _, s := range statuses {
		byTarget[s.Target] = append(byTarget[s.Target], s)
		if hasStatusError(s) {
			errored++
		}
		if isEmptyRepository(s) {
			empty++
		}
		if s.Dirty {
			dirty++
		}
		if s.Ahead > 0 {
			ahead++
		}
		if s.Behind > 0 {
			behind++
			if !s.CanFastForward {
				diverged++
			}
		}
		if s.Archived {
			archived++
		}
		if s.Orphan {
			orphan++
		}
		if s.Missing {
			missing++
		}
		if statusGroupFor(s) == groupClean {
			clean++
		}
	}

	for _, target := range targets {
		fmt.Printf("Target: %s  %s\n\n", target.Name, target.Path)
		targetStatuses := byTarget[target.Name]
		if len(targetStatuses) == 0 {
			fmt.Println("No repositories found.")
			fmt.Println()
			continue
		}
		renderStatusGroups(target, targetStatuses, opts.ShowAll)
	}

	repositoryWord := "repositories"
	if len(statuses) == 1 {
		repositoryWord = "repository"
	}
	fmt.Printf("Summary: %d %s: %d clean, %d empty, %d dirty, %d ahead, %d behind, %d diverged, %d archived, %d orphan, %d missing, %d errors\n",
		len(statuses), repositoryWord, clean, empty, dirty, ahead, behind, diverged, archived, orphan, missing, errored)

	if opts.Debug && len(timings) > 0 {
		totalTime := time.Duration(0)
		for _, t := range timings {
			totalTime += t.Total
		}
		fmt.Printf("\nDebug: %d repos, total time %v\n", len(timings), totalTime)
	}
	return nil
}

func hasStatusError(s RepoStatus) bool {
	return s.Error != "" || s.RemoteError != "" || s.MetadataError != ""
}

func statusErrorMessage(s RepoStatus) string {
	var messages []string
	if s.Error != "" {
		messages = append(messages, s.Error)
	}
	if s.MetadataError != "" {
		messages = append(messages, "provider: "+s.MetadataError)
	}
	if s.RemoteError != "" {
		messages = append(messages, "remote: "+s.RemoteError)
	}
	return strings.Join(messages, "; ")
}

func statusGroupFor(s RepoStatus) statusGroup {
	if s.IdentityIssue != "" {
		return groupAttention
	}
	if s.Archived {
		return groupArchived
	}
	if hasStatusError(s) || s.Orphan || s.Dirty || s.Ahead > 0 || s.Behind > 0 {
		return groupAttention
	}
	if s.Missing {
		return groupMissing
	}
	if isEmptyRepository(s) {
		return groupEmpty
	}
	return groupClean
}

func primaryStatus(s RepoStatus) string {
	if s.Transferred {
		return "TRANSFERRED"
	}
	if s.IdentityIssue != "" {
		if s.RemoteName != "" {
			return "RENAMED"
		}
		return "IDENTITY"
	}
	if s.Archived {
		return "ARCHIVED"
	}
	if hasStatusError(s) {
		return "ERROR"
	}
	if s.Orphan {
		return "ORPHAN"
	}
	if s.Behind > 0 && !s.CanFastForward {
		return "DIVERGED"
	}
	if s.Dirty {
		return "DIRTY"
	}
	if s.Behind > 0 {
		return "BEHIND"
	}
	if s.Ahead > 0 {
		return "AHEAD"
	}
	if s.Missing {
		return "MISSING"
	}
	if isEmptyRepository(s) {
		return "EMPTY"
	}
	return "CLEAN"
}

func statusDetails(s RepoStatus, state string) string {
	var details []string
	if s.IdentityIssue != "" {
		details = append(details, s.IdentityIssue)
	}
	if s.Archived && state != "ARCHIVED" {
		details = append(details, "archived")
	}
	if s.Error != "" {
		details = append(details, s.Error)
	}
	if s.MetadataError != "" {
		details = append(details, "provider: "+s.MetadataError)
	}
	if s.RemoteError != "" {
		details = append(details, "remote: "+s.RemoteError)
	}
	if s.Orphan && state != "ORPHAN" {
		details = append(details, "orphan")
	}
	if s.Missing && state != "MISSING" {
		details = append(details, "missing")
	}
	if isEmptyRepository(s) && state != "EMPTY" {
		details = append(details, "empty")
	}
	if s.Dirty && state != "DIRTY" {
		details = append(details, "dirty")
	}
	if s.Ahead > 0 {
		details = append(details, fmt.Sprintf("%d ahead", s.Ahead))
	}
	if s.Behind > 0 {
		details = append(details, fmt.Sprintf("%d behind", s.Behind))
	}
	if s.Behind > 0 && !s.CanFastForward && state != "DIVERGED" {
		details = append(details, "diverged")
	}
	return strings.Join(details, ", ")
}

func renderStatusGroups(target config.Target, statuses []RepoStatus, showAll bool) {
	groups := []struct {
		kind  statusGroup
		title string
	}{
		{groupArchived, "Archived"},
		{groupAttention, "Attention"},
		{groupMissing, "Missing"},
		{groupEmpty, "Empty"},
		{groupClean, "Clean"},
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	for _, group := range groups {
		var rows []RepoStatus
		for _, s := range statuses {
			if statusGroupFor(s) == group.kind {
				rows = append(rows, s)
			}
		}
		if len(rows) == 0 {
			continue
		}
		sort.Slice(rows, func(i, j int) bool {
			return rows[i].Path < rows[j].Path
		})
		if group.kind == groupClean && !showAll {
			fmt.Fprintf(w, "Clean (%d hidden; use --all)\n\n", len(rows))
			continue
		}

		fmt.Fprintf(w, "%s (%d)\n", group.title, len(rows))
		for _, s := range rows {
			relativePath, err := filepath.Rel(target.Path, s.Path)
			if err != nil || relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) {
				relativePath = s.Path
			}
			branch := s.Branch
			if branch == "" {
				branch = s.DefaultBranch
			}
			if branch == "" {
				branch = "-"
			}
			state := primaryStatus(s)
			details := statusDetails(s, state)
			if details == "" {
				fmt.Fprintf(w, "  %s\t%s\t%s\n", state, relativePath, branch)
			} else {
				fmt.Fprintf(w, "  %s\t%s\t%s\t%s\n", state, relativePath, branch, details)
			}
		}
		fmt.Fprintln(w)
	}
	w.Flush()
}

func discoverStatusJobs(targets []config.Target, cfg *config.Config) ([]statusJob, []orgKey, error) {
	var jobs []statusJob
	var orgKeys []orgKey
	orgKeySet := make(map[string]bool)

	for _, t := range targets {
		tok := cfg.Providers[t.Provider].Token
		if t.Repo == "" {
			if _, err := os.Stat(t.Path); os.IsNotExist(err) {
				return nil, nil, fmt.Errorf("target %q path does not exist: %s", t.Name, t.Path)
			}
			entries, err := os.ReadDir(t.Path)
			if err != nil {
				return nil, nil, fmt.Errorf("reading target %s: %w", t.Path, err)
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
			parentExists := isGitRepo(t.Path)
			jobs = append(jobs, statusJob{path: t.Path, target: t.Name, name: t.Repo, org: t.Org, provider: t.Provider, token: tok, missing: !parentExists, fixedPath: true})
			// foldout
			if parentExists {
				fc, err := loadFoldout(t.Path)
				if err != nil {
					return nil, nil, err
				}
				if fc != nil {
					if err := cleanFoldoutTargets(t.Path, fc.Repos); err != nil {
						return nil, nil, err
					}
					for _, fr := range fc.Repos {
						dest := filepath.Join(t.Path, fr.Target)
						parts := strings.Split(fr.Name, "/")
						repoName := parts[len(parts)-1]
						frOrg := t.Org
						if len(parts) == 2 {
							frOrg = parts[0]
						}
						jobs = append(jobs, statusJob{path: dest, target: t.Name, name: repoName, org: frOrg, provider: t.Provider, token: tok, missing: !isGitRepo(dest), fixedPath: true})
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

	return jobs, orgKeys, nil
}

func (m *Manager) getAllStatuses(targets []config.Target, debug bool, workers int, progress *progressReporter) ([]RepoStatus, []RepoTiming, error) {
	return m.scanStatuses(targets, debug, workers, progress)
}

func (m *Manager) scanStatuses(targets []config.Target, debug bool, workers int, progress *progressReporter) ([]RepoStatus, []RepoTiming, error) {
	jobs, orgKeys, err := discoverStatusJobs(targets, m.config)
	if err != nil {
		return nil, nil, err
	}
	index, metadataErrors := m.buildRepoIndex(orgKeys, progress)
	probe := &historyProbe{progress: progress}
	defer probe.close()
	progress.beginChecks(len(jobs))
	metadata := &scanMetadata{index: index, errors: metadataErrors, readOnly: true}
	results := pool.Run(jobs, workers, func(job statusJob) statusResult {
		return m.scanStatus(job, metadata, probe, progress, nil)
	})
	statuses := make([]RepoStatus, len(results))
	timings := make([]RepoTiming, len(results))
	for i, r := range results {
		statuses[i] = r.status
		timings[i] = r.timing
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
	// Clear inherited credential helpers first, then inject an inline helper
	// that echoes the token. This avoids mutating .git/config and prevents
	// stale global helpers from winning before Tugboat's token is tried.
	helper := fmt.Sprintf("!f() { echo username=x-access-token; echo password=%s; }; f", token)
	env = append(env,
		"GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=credential.helper",
		"GIT_CONFIG_VALUE_0=",
		"GIT_CONFIG_KEY_1=credential.helper",
		"GIT_CONFIG_VALUE_1="+helper,
	)
	return env
}

// ------------ git helpers --------------

func isGitRepo(path string) bool {
	gitDir := filepath.Join(path, ".git")
	info, err := os.Stat(gitDir)
	return err == nil && info.IsDir()
}

// getCurrentBranch returns the checked-out branch, including for an unborn
// branch that does not have a commit yet. Detached HEADs retain the historical
// "HEAD" result from rev-parse.
func getCurrentBranch(repoPath string) (string, error) {
	branch, err := gitOutput(repoPath, "rev-parse", "--abbrev-ref", "HEAD")
	if err == nil {
		return strings.TrimSpace(branch), nil
	}
	branch, symbolicErr := gitOutput(repoPath, "symbolic-ref", "--quiet", "--short", "HEAD")
	if symbolicErr != nil {
		return "", err
	}
	return strings.TrimSpace(branch), nil
}

func getRepoStatus(path, target, org, name, provider, token string, timing *RepoTiming) RepoStatus {
	return getRepoStatusWithProgress(path, target, org, name, provider, token, timing, nil)
}

func getRepoStatusWithProgress(path, target, org, name, provider, token string, timing *RepoTiming, progress *progressReporter) RepoStatus {
	return getRepoStatusWithProgressAndFetch(path, target, org, name, provider, token, timing, progress, true)
}

func getRepoStatusWithProgressAndFetch(path, target, org, name, provider, token string, timing *RepoTiming, progress *progressReporter, fetch bool) RepoStatus {
	totalStart := time.Now()
	status := RepoStatus{
		Path:     path,
		Target:   target,
		Provider: provider,
		Org:      org,
		Name:     name,
	}

	// Get current branch
	progress.detail("  [DETAIL] %s: reading branch\n", path)
	branchStart := time.Now()
	branch, err := getCurrentBranch(path)
	if timing != nil {
		timing.Branch = time.Since(branchStart)
	}
	if err != nil {
		status.Error = fmt.Sprintf("getting branch: %v", err)
		return status
	}
	status.Branch = branch
	status.Unborn = gitRun(path, "rev-parse", "--verify", "--quiet", "HEAD") != nil

	if fetch {
		// Fetch from remote
		progress.detail("  [DETAIL] %s: git fetch\n", path)
		fetchStart := time.Now()
		if fetchErr := gitFetchWithStderr(path, token); fetchErr != "" {
			status.RemoteError = fetchErr
		}
		if timing != nil {
			timing.Fetch = time.Since(fetchStart)
		}
		if status.Unborn && status.RemoteError == "" && !originHasBranches(path) {
			status.RemoteEmpty = true
		}

	}

	// Check for uncommitted changes
	progress.detail("  [DETAIL] %s: git status\n", path)
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

	if !fetch {
		return status
	}

	// Get ahead/behind counts
	progress.detail("  [DETAIL] %s: counting ahead/behind commits\n", path)
	revListStart := time.Now()
	upstream := fmt.Sprintf("origin/%s", status.Branch)
	var revList string
	if status.Unborn {
		if remoteTrackingRefExists(path, status.Branch) {
			revList, err = gitOutput(path, "rev-list", "--count", upstream)
			if err == nil {
				fmt.Sscanf(strings.TrimSpace(revList), "%d", &status.Behind)
			}
		}
	} else {
		revList, err = gitOutput(path, "rev-list", "--left-right", "--count", fmt.Sprintf("%s...%s", status.Branch, upstream))
		if err == nil {
			parts := strings.Fields(strings.TrimSpace(revList))
			if len(parts) == 2 {
				fmt.Sscanf(parts[0], "%d", &status.Ahead)
				fmt.Sscanf(parts[1], "%d", &status.Behind)
			}
		} else if status.RemoteError == "" {
			// rev-list failed after a successful fetch — the upstream ref is gone.
			status.UpstreamGone = true
			if count, countErr := gitOutput(path, "rev-list", "--count", status.Branch); countErr == nil {
				fmt.Sscanf(strings.TrimSpace(count), "%d", &status.LocalCommits)
			}
		}
	}
	if timing != nil {
		timing.RevList = time.Since(revListStart)
	}

	progress.detail("  [DETAIL] %s: checking fast-forward ancestry\n", path)
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
	branch, err := getCurrentBranch(repoPath)
	if err != nil {
		return false, "", fmt.Errorf("getting branch: %w", err)
	}
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

func originHasBranches(repoPath string) bool {
	refs, err := gitOutput(repoPath, "for-each-ref", "--format=%(refname)", "refs/remotes/origin")
	return err == nil && strings.TrimSpace(refs) != ""
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
	if s.Unborn {
		if err := ensureLocalBranch(s.Path, defaultBranch); err != nil {
			return s, false, err
		}
		refreshed := getRepoStatus(s.Path, s.Target, s.Org, s.Name, s.Provider, token, nil)
		refreshed.DefaultBranch = defaultBranch
		refreshed.Archived = s.Archived
		refreshed.Orphan = s.Orphan
		refreshed.gitDirInfo = s.gitDirInfo
		refreshed.identity, refreshed.repository, refreshed.RepositoryID, refreshed.RemoteName = s.identity, s.repository, s.RepositoryID, s.RemoteName
		return refreshed, true, nil
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
	refreshed.gitDirInfo = s.gitDirInfo
	refreshed.identity, refreshed.repository, refreshed.RepositoryID, refreshed.RemoteName = s.identity, s.repository, s.RepositoryID, s.RemoteName
	return refreshed, true, nil
}

func isEmptyRepository(s RepoStatus) bool {
	return s.Unborn && s.RemoteEmpty
}

type archiveSkipError struct {
	reason string
}

func (e *archiveSkipError) Error() string { return e.reason }

func archiveSkip(reason string) error {
	return &archiveSkipError{reason: reason}
}

func gitOutputWithAuth(repoPath, token string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = repoPath
	cmd.Env = gitEnvWithAuth(token)
	output, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message != "" {
			return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, message)
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(output), nil
}

func normalizeGitRemoteURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	// SCP-like SSH URLs (git@example.com:org/repo.git) are not parsed as
	// hierarchical URLs by net/url.
	if !strings.Contains(raw, "://") {
		if colon := strings.Index(raw, ":"); colon > 0 && strings.Contains(raw[:colon], "@") {
			host := raw[:colon]
			if at := strings.LastIndex(host, "@"); at >= 0 {
				host = host[at+1:]
			}
			path := strings.TrimSuffix(strings.TrimPrefix(raw[colon+1:], "/"), ".git")
			return strings.ToLower(host) + "/" + path
		}
		if absolute, err := filepath.Abs(raw); err == nil {
			return filepath.Clean(absolute)
		}
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if parsed.Scheme == "file" {
		return filepath.Clean(parsed.Path)
	}
	host := strings.ToLower(parsed.Hostname())
	if parsed.Port() != "" {
		host += ":" + parsed.Port()
	}
	path := strings.TrimSuffix(strings.TrimPrefix(parsed.Path, "/"), ".git")
	if host == "" {
		return filepath.Clean(parsed.Path)
	}
	return host + "/" + path
}

func validateArchiveRemovalPath(candidate string, target config.Target) error {
	candidateAbs, err := filepath.Abs(candidate)
	if err != nil {
		return fmt.Errorf("resolving checkout path: %w", err)
	}
	rootAbs, err := filepath.Abs(target.Path)
	if err != nil {
		return fmt.Errorf("resolving target path: %w", err)
	}
	if candidateAbs == filepath.VolumeName(candidateAbs)+string(filepath.Separator) {
		return fmt.Errorf("refusing to remove filesystem root %s", candidateAbs)
	}

	info, err := os.Lstat(candidateAbs)
	if err != nil {
		return fmt.Errorf("inspecting checkout path: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("checkout path is not a real directory: %s", candidateAbs)
	}
	candidateReal, err := filepath.EvalSymlinks(candidateAbs)
	if err != nil {
		return fmt.Errorf("resolving checkout symlinks: %w", err)
	}
	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return fmt.Errorf("resolving target symlinks: %w", err)
	}
	relative, err := filepath.Rel(rootReal, candidateReal)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("checkout %s is outside target %s", candidateAbs, rootAbs)
	}
	if target.Repo == "" && relative == "." {
		return fmt.Errorf("refusing to remove organization target root %s", rootAbs)
	}
	if !isGitRepo(candidateAbs) {
		return fmt.Errorf("checkout is not a standard Git repository: %s", candidateAbs)
	}
	return nil
}

func verifyOriginMatches(repoPath string, repository *remote.Repository) error {
	origin, err := gitOutput(repoPath, "remote", "get-url", "origin")
	if err != nil {
		return fmt.Errorf("reading origin URL: %w", err)
	}
	actual := normalizeGitRemoteURL(origin)
	expectedURLs := []string{repository.CloneURL, repository.SSHURL, repository.HTMLURL}
	for _, expectedURL := range expectedURLs {
		expected := normalizeGitRemoteURL(expectedURL)
		if expected != "" && strings.EqualFold(actual, expected) {
			return nil
		}
	}
	return fmt.Errorf("origin %q does not match provider repository %s", strings.TrimSpace(origin), repository.FullName)
}

func (m *Manager) confirmArchivedRepository(s RepoStatus) (*remote.Repository, error) {
	if s.identity == nil || s.RepositoryID <= 0 || s.identity.Pending != nil || s.IdentityIssue != "" {
		return nil, archiveSkip("repository identity is unresolved or a replacement is pending")
	}
	if err := confirmCheckoutDirectory(s); err != nil {
		return nil, err
	}
	stored, err := readIdentity(s.Path)
	if err != nil {
		return nil, err
	}
	if stored == nil || stored.RepositoryID != s.RepositoryID || stored.Pending != nil || stored.ProviderType != s.identity.ProviderType || stored.APIURL != s.identity.APIURL {
		return nil, archiveSkip("local repository identity changed or a replacement is pending")
	}
	client, ok := m.providers[s.Provider]
	if !ok {
		return nil, fmt.Errorf("no client for provider %s", s.Provider)
	}
	repository, err := remote.GetRepoFresh(client, s.Org, s.RemoteName)
	if err != nil {
		return nil, fmt.Errorf("checking archive state for %s/%s: %w", s.Org, s.Name, err)
	}
	if repository == nil {
		return nil, fmt.Errorf("provider repository %s/%s no longer exists", s.Org, s.Name)
	}
	if repository.FullName != "" && !strings.EqualFold(repository.FullName, s.Org+"/"+s.RemoteName) {
		return nil, fmt.Errorf("provider returned unexpected repository %s", repository.FullName)
	}
	if repository.ID != s.RepositoryID {
		return nil, fmt.Errorf("provider identity changed before archive removal")
	}
	if !repository.Archived {
		return nil, archiveSkip("repository is no longer archived")
	}
	return repository, nil
}

func refreshArchiveRemote(repoPath, token string) ([]string, error) {
	if _, err := gitOutputWithAuth(repoPath, token, "fetch", "--quiet", "--prune", "origin", "+refs/heads/*:refs/remotes/origin/*"); err != nil {
		return nil, err
	}
	if _, err := gitOutputWithAuth(repoPath, token, "fetch", "--quiet", "--tags", "origin"); err != nil {
		return nil, err
	}
	output, err := gitOutputWithAuth(repoPath, token, "ls-remote", "--heads", "--tags", "origin")
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var objectIDs []string
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || seen[fields[0]] {
			continue
		}
		seen[fields[0]] = true
		objectIDs = append(objectIDs, fields[0])
	}
	return objectIDs, nil
}

func localUnpublishedWork(repoPath string, remoteObjectIDs []string) (unpublishedWork, error) {
	refs, err := gitOutput(repoPath, "for-each-ref", "--format=%(refname)")
	if err != nil {
		return unpublishedWork{}, fmt.Errorf("listing local refs: %w", err)
	}
	var localRefs []localWorkRef
	for _, ref := range strings.Fields(refs) {
		if !strings.HasPrefix(ref, "refs/remotes/") {
			localRefs = append(localRefs, localWorkRef{ref: ref, label: localWorkLabel(ref)})
		}
	}
	if gitRun(repoPath, "rev-parse", "--verify", "--quiet", "HEAD") == nil {
		localRefs = append(localRefs, localWorkRef{ref: "HEAD", label: checkoutHeadLabel(repoPath)})
	}
	return inspectUnpublishedWork(repoPath, localRefs, remoteObjectIDs)
}

func activeGitOperation(repoPath string) (string, error) {
	markers := []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "BISECT_LOG", "rebase-apply", "rebase-merge", "sequencer"}
	for _, marker := range markers {
		gitPath, err := gitOutput(repoPath, "rev-parse", "--git-path", marker)
		if err != nil {
			return "", fmt.Errorf("resolving Git operation marker %s: %w", marker, err)
		}
		gitPath = strings.TrimSpace(gitPath)
		if !filepath.IsAbs(gitPath) {
			gitPath = filepath.Join(repoPath, gitPath)
		}
		if _, err := os.Stat(gitPath); err == nil {
			return marker, nil
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("inspecting Git operation marker %s: %w", marker, err)
		}
	}
	return "", nil
}

// Missing, unlocked branch worktrees marked prunable by Git contain no
// worktree files. Their commits remain protected through the branch ref. Keep
// live, locked, detached, or unresolvable registrations as cleanup blockers.
func hasLinkedWorktree(repoPath string) (bool, error) {
	output, err := gitOutput(repoPath, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return false, fmt.Errorf("listing linked worktrees: %w", err)
	}
	for i, record := range strings.Split(strings.TrimSuffix(output, "\x00\x00"), "\x00\x00") {
		if i == 0 {
			continue
		}
		var dir, head, branch string
		var prunable, locked bool
		for _, field := range strings.Split(record, "\x00") {
			switch {
			case strings.HasPrefix(field, "worktree "):
				dir = strings.TrimPrefix(field, "worktree ")
			case strings.HasPrefix(field, "HEAD "):
				head = strings.TrimPrefix(field, "HEAD ")
			case strings.HasPrefix(field, "branch "):
				branch = strings.TrimPrefix(field, "branch ")
			case field == "prunable" || strings.HasPrefix(field, "prunable "):
				prunable = true
			case field == "locked" || strings.HasPrefix(field, "locked "):
				locked = true
			}
		}
		if dir == "" || !prunable || locked || head == "" || branch == "" {
			return true, nil
		}
		if _, err := os.Lstat(dir); err == nil {
			return true, nil
		} else if !os.IsNotExist(err) {
			return false, fmt.Errorf("checking linked worktree %s: %w", dir, err)
		}
		if gitRun(repoPath, "merge-base", "--is-ancestor", head, branch) != nil {
			return true, nil
		}
	}
	return false, nil
}

func nestedGitCheckout(repoPath string) (string, error) {
	rootGit := filepath.Join(repoPath, ".git")
	var nested string
	err := filepath.WalkDir(repoPath, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == rootGit {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if path != repoPath && entry.Name() == ".git" {
			nested = filepath.Dir(path)
			if entry.IsDir() {
				return filepath.SkipDir
			}
		}
		return nil
	})
	return nested, err
}

func verifyArchivedLocalState(repoPath string, remoteObjectIDs []string) error {
	dirty, err := gitOutput(repoPath, "status", "--porcelain=v1", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return fmt.Errorf("checking worktree status: %w", err)
	}
	if strings.TrimSpace(dirty) != "" {
		return archiveSkip("dirty worktree")
	}
	if operation, err := activeGitOperation(repoPath); err != nil {
		return err
	} else if operation != "" {
		return archiveSkip("active Git operation: " + operation)
	}
	if linked, err := hasLinkedWorktree(repoPath); err != nil {
		return err
	} else if linked {
		return archiveSkip("has linked worktrees")
	}
	if nested, err := nestedGitCheckout(repoPath); err != nil {
		return fmt.Errorf("checking nested repositories: %w", err)
	} else if nested != "" {
		return archiveSkip("contains nested Git checkout " + nested)
	}
	localOnly, err := localUnpublishedWork(repoPath, remoteObjectIDs)
	if err != nil {
		return err
	}
	if localOnly.count > 0 {
		return archiveSkip(localOnly.reason())
	}
	return nil
}

func fastForwardArchivedDefault(repoPath, defaultBranch string, remoteEmpty bool, token string) (bool, error) {
	if remoteEmpty {
		return false, nil
	}
	defaultBranch = strings.TrimSpace(defaultBranch)
	if defaultBranch == "" {
		resolved, err := defaultBranchFromOriginHead(repoPath)
		if err != nil {
			return false, err
		}
		defaultBranch = resolved
	}
	if !remoteTrackingRefExists(repoPath, defaultBranch) {
		return false, fmt.Errorf("default branch %q is unavailable on origin", defaultBranch)
	}

	branch, err := getCurrentBranch(repoPath)
	if err != nil {
		return false, fmt.Errorf("getting current branch: %w", err)
	}
	if branch != defaultBranch {
		if err := switchToDefaultBranch(repoPath, branch, defaultBranch); err != nil {
			var skipErr *updateSkipError
			if errors.As(err, &skipErr) {
				return false, archiveSkip(skipErr.reason)
			}
			return false, err
		}
	}

	localHead, localErr := gitOutput(repoPath, "rev-parse", "--verify", "HEAD")
	remoteHead, err := gitOutput(repoPath, "rev-parse", "--verify", "origin/"+defaultBranch)
	if err != nil {
		return false, fmt.Errorf("reading origin/%s: %w", defaultBranch, err)
	}
	if localErr == nil && strings.TrimSpace(localHead) == strings.TrimSpace(remoteHead) {
		return false, nil
	}

	var output string
	if localErr != nil {
		cmd := exec.Command("git", "pull", "--ff-only", "origin", defaultBranch)
		cmd.Dir = repoPath
		cmd.Env = gitEnvWithAuth(token)
		combined, pullErr := cmd.CombinedOutput()
		output, err = string(combined), pullErr
	} else {
		cmd := exec.Command("git", "merge", "--ff-only", "origin/"+defaultBranch)
		cmd.Dir = repoPath
		cmd.Env = gitEnvNoPrompt()
		combined, mergeErr := cmd.CombinedOutput()
		output, err = string(combined), mergeErr
	}
	if err != nil {
		return false, archiveSkip("default branch cannot be fast-forwarded: " + strings.TrimSpace(output))
	}

	localHead, err = gitOutput(repoPath, "rev-parse", "--verify", "HEAD")
	if err != nil || strings.TrimSpace(localHead) != strings.TrimSpace(remoteHead) {
		return false, fmt.Errorf("default branch did not reach origin/%s", defaultBranch)
	}
	return true, nil
}

func (m *Manager) removeArchivedRepo(s RepoStatus, target config.Target, token string) (bool, error) {
	if err := validateArchiveRemovalPath(s.Path, target); err != nil {
		return false, err
	}
	repository, err := m.confirmArchivedRepository(s)
	if err != nil {
		return false, err
	}
	if err := verifyOriginMatches(s.Path, repository); err != nil {
		return false, err
	}
	remoteObjectIDs, err := refreshArchiveRemote(s.Path, token)
	if err != nil {
		return false, err
	}
	if err := verifyArchivedLocalState(s.Path, remoteObjectIDs); err != nil {
		return false, err
	}
	fastForwarded, err := fastForwardArchivedDefault(s.Path, repository.DefaultBranch, repository.Empty, token)
	if err != nil {
		return false, err
	}
	if err := verifyArchivedLocalState(s.Path, remoteObjectIDs); err != nil {
		return false, err
	}

	repository, err = m.confirmArchivedRepository(s)
	if err != nil {
		return false, err
	}
	if err := verifyOriginMatches(s.Path, repository); err != nil {
		return false, err
	}
	if err := validateArchiveRemovalPath(s.Path, target); err != nil {
		return false, err
	}
	if err := verifyArchivedLocalState(s.Path, remoteObjectIDs); err != nil {
		return false, err
	}
	if err := os.RemoveAll(s.Path); err != nil {
		return false, fmt.Errorf("removing checkout: %w", err)
	}
	return fastForwarded, nil
}

// Pull and Push are internal convenience entry points for the shared sync engine.
func (m *Manager) Pull(targetNames []string, workers int) error {
	return m.Sync(targetNames, SyncOptions{Mode: SyncPull, Workers: workers})
}

func (m *Manager) Push(targetNames []string, workers int) error {
	return m.Sync(targetNames, SyncOptions{Mode: SyncPush, Workers: workers})
}

func (m *Manager) Sync(targetNames []string, runOpts SyncOptions) error {
	targets, err := m.targetsFor(targetNames)
	if err != nil {
		return err
	}
	if runOpts.Mode < SyncBoth || runOpts.Mode > SyncCloneOnly {
		return fmt.Errorf("invalid sync mode")
	}
	if runOpts.Mode == SyncCloneOnly {
		if runOpts.RemoveArchived {
			return fmt.Errorf("--remove-archived cannot be combined with --clone-only")
		}
		return m.cloneOnly(targetNames, runOpts.ExcludeEmpty, runOpts.IncludeArchived, runOpts.Workers)
	}
	if runOpts.ExcludeEmpty || runOpts.IncludeArchived {
		return fmt.Errorf("--exclude-empty and --include-archived require --clone-only")
	}
	if runOpts.Mode == SyncPush && runOpts.RemoveArchived {
		return fmt.Errorf("--remove-archived cannot be combined with --push")
	}
	for _, t := range targets {
		if err := t.ValidateExclusions(); err != nil {
			return err
		}
	}
	progress := &progressReporter{out: os.Stdout, verbose: m.Verbose}
	action, label, word := "sync", "Sync", "synced"
	if runOpts.Mode == SyncPull {
		action, label, word = "pull", "Pull", "pulled"
	}
	if runOpts.Mode == SyncPush {
		action, label, word = "push", "Push", "pushed"
	}
	progress.printf("%s: discovering repositories...\n", label)
	if runOpts.Mode != SyncPush {
		for _, t := range targets {
			if t.Repo == "" {
				if err := os.MkdirAll(t.Path, 0755); err != nil {
					return err
				}
			}
		}
	} else {
		var existing []config.Target
		for _, t := range targets {
			if t.Repo != "" {
				existing = append(existing, t)
			} else if _, err := os.Stat(t.Path); !os.IsNotExist(err) {
				existing = append(existing, t)
			}
		}
		targets = existing
	}

	// Map target names to provider settings and target boundaries.
	optMap := make(map[string]config.ProviderOptions)
	tokenMap := make(map[string]string)
	targetMap := make(map[string]config.Target)
	for _, t := range targets {
		optMap[t.Name] = m.config.Providers[t.Provider].Options
		tokenMap[t.Name] = m.config.Providers[t.Provider].Token
		targetMap[t.Name] = t
	}

	var archivedStatuses []RepoStatus
	var synced, removed, skipped, failed atomic.Int64
	var resultMu sync.Mutex
	seen := make(map[string]bool)
	process := func(s RepoStatus) {
		resultMu.Lock()
		if seen[s.Path] {
			resultMu.Unlock()
			return
		}
		seen[s.Path] = true
		resultMu.Unlock()
		if !(s.Archived && runOpts.RemoveArchived && !hasStatusError(s) && !s.Missing) {
			progress.detail("  [START] %s: %s\n", s.Path, action)
		}
		if s.Removed {
			progress.finish("  [REMOVE] %s: obsolete checkout safely removed\n", s.Path)
			removed.Add(1)
			return
		}
		opts := optMap[s.Target]
		tok := tokenMap[s.Target]

		if hasStatusError(s) {
			progress.finish("  [ERROR] %s: %s\n", s.Path, statusErrorMessage(s))
			failed.Add(1)
			return
		}
		if s.Missing {
			progress.finish("  [SKIP]  %s: missing\n", s.Path)
			skipped.Add(1)
			return
		}
		if s.IdentityIssue != "" || s.Orphan {
			issue := strings.Replace(s.IdentityIssue, "; run tugboat sync "+s.Target, "", 1)
			progress.finish("  [SKIP]  %s: %s\n", s.Path, issue)
			skipped.Add(1)
			return
		}
		if s.Archived {
			if runOpts.RemoveArchived {
				resultMu.Lock()
				archivedStatuses = append(archivedStatuses, s)
				resultMu.Unlock()
			} else {
				progress.finish("  [SKIP]  %s: archived\n", s.Path)
				skipped.Add(1)
			}
			return
		}
		if isEmptyRepository(s) {
			progress.finish("  [SKIP]  %s: no commits locally or on origin\n", s.Path)
			skipped.Add(1)
			return
		}
		if runOpts.Mode == SyncPush {
			if s.Behind > 0 {
				progress.finish("  [SKIP]  %s: behind remote, pull first\n", s.Path)
				skipped.Add(1)
			} else if s.Ahead == 0 {
				progress.finish("  [OK]    %s: nothing to push\n", s.Path)
			} else if err := m.confirmStatusIdentity(s); err != nil {
				progress.finish("  [ERROR] %s: %v\n", s.Path, err)
				failed.Add(1)
			} else if err := gitPush(s.Path, tok); err != nil {
				progress.finish("  [ERROR] %s: %v\n", s.Path, err)
				failed.Add(1)
			} else {
				progress.finish("  [PUSH]  %s: %d commits\n", s.Path, s.Ahead)
				synced.Add(1)
			}
			return
		}
		if runOpts.Mode == SyncPull && s.RemoteEmpty {
			progress.finish("  [SKIP]  %s: origin has no commits\n", s.Path)
			skipped.Add(1)
			return
		}
		if err := m.confirmStatusIdentity(s); err != nil {
			progress.finish("  [ERROR] %s: %v\n", s.Path, err)
			failed.Add(1)
			return
		}
		prepared, switched, err := m.prepareRepoForDefaultBranch(s, tok)
		if err != nil {
			var skipErr *updateSkipError
			if errors.As(err, &skipErr) {
				progress.finish("  [SKIP]  %s: %s\n", s.Path, skipErr.reason)
				skipped.Add(1)
				return
			}
			progress.finish("  [ERROR] %s: %v\n", s.Path, err)
			failed.Add(1)
			return
		}
		if switched {
			progress.note(s.Path, "switched "+s.Branch+" -> "+prepared.DefaultBranch)
			progress.detail("  [SWITCH] %s: %s -> %s\n", s.Path, s.Branch, prepared.DefaultBranch)
		}
		if hasStatusError(prepared) {
			progress.finish("  [ERROR] %s: %s\n", prepared.Path, statusErrorMessage(prepared))
			failed.Add(1)
			return
		}
		if prepared.Dirty {
			progress.finish("  [SKIP]  %s: dirty\n", prepared.Path)
			skipped.Add(1)
			return
		}

		if prepared.Behind > 0 {
			if !prepared.CanFastForward && opts.Sync.GetFFOnly() {
				// Diverged: ff-only would fail, go straight to rebase.
				progress.detail("  [REBASE] %s: %d behind, %d ahead (diverged)\n", prepared.Path, prepared.Behind, prepared.Ahead)
				if err := gitPullRebase(prepared.Path, tok); err != nil {
					progress.finish("  [ERROR] %s: %v\n", prepared.Path, err)
					failed.Add(1)
					return
				}
			} else {
				progress.detail("  [PULL]  %s: %d behind\n", prepared.Path, prepared.Behind)
				if err := gitPull(prepared.Path, opts.Sync.GetFFOnly(), tok); err != nil {
					progress.finish("  [ERROR] %s: %v\n", prepared.Path, err)
					failed.Add(1)
					return
				}
			}
		}
		if prepared.Ahead > 0 && runOpts.Mode != SyncPull {
			progress.detail("  [PUSH]  %s: %d ahead\n", prepared.Path, prepared.Ahead)
			if err := gitPush(prepared.Path, tok); err != nil {
				progress.finish("  [ERROR] %s: %v\n", prepared.Path, err)
				failed.Add(1)
				return
			}
		}
		if prepared.Behind == 0 && (prepared.Ahead == 0 || runOpts.Mode == SyncPull) {
			message := "up to date"
			if prepared.Ahead > 0 {
				message = "nothing to pull; local commits retained"
			}
			progress.finish("  [OK]    %s: %s\n", prepared.Path, message)
		} else if runOpts.Mode == SyncPull {
			progress.finish("  [PULL]  %s\n", prepared.Path)
			synced.Add(1)
		} else {
			progress.finish("  [SYNC]  %s\n", prepared.Path)
		}
		if runOpts.Mode != SyncPull {
			synced.Add(1)
		}
	}

	alreadySeen := func(path string) bool { resultMu.Lock(); defer resultMu.Unlock(); return seen[path] }
	if err := m.streamSync(targets, runOpts, progress, alreadySeen, process); err != nil {
		return err
	}

	// A parent checkout may contain foldouts. Remove deepest paths first, then
	// let the nested-repository safety check decide whether a parent can follow.
	sort.Slice(archivedStatuses, func(i, j int) bool {
		leftDepth := strings.Count(filepath.Clean(archivedStatuses[i].Path), string(filepath.Separator))
		rightDepth := strings.Count(filepath.Clean(archivedStatuses[j].Path), string(filepath.Separator))
		if leftDepth == rightDepth {
			return archivedStatuses[i].Path < archivedStatuses[j].Path
		}
		return leftDepth > rightDepth
	})
	for _, s := range archivedStatuses {
		progress.detail("  [START] %s: checking archived checkout for removal\n", s.Path)
		fastForwarded, err := m.removeArchivedRepo(s, targetMap[s.Target], tokenMap[s.Target])
		if err != nil {
			var skipErr *archiveSkipError
			if errors.As(err, &skipErr) {
				progress.finish("  [SKIP]  %s: archived, %s\n", s.Path, skipErr.reason)
				skipped.Add(1)
				continue
			}
			progress.finish("  [ERROR] %s: %v\n", s.Path, err)
			failed.Add(1)
			continue
		}
		if fastForwarded {
			progress.note(s.Path, "fast-forwarded archived default branch")
			progress.detail("  [PULL]  %s: fast-forwarded archived default branch\n", s.Path)
		}
		progress.finish("  [REMOVE] %s: archived checkout removed\n", s.Path)
		removed.Add(1)
	}

	if runOpts.RemoveArchived || removed.Load() > 0 {
		fmt.Printf("%s complete: %d %s, %d removed, %d skipped, %d failed\n", label, synced.Load(), word, removed.Load(), skipped.Load(), failed.Load())
		if failed.Load() > 0 {
			return fmt.Errorf("sync completed with %d operational errors", failed.Load())
		}
		return nil
	}
	fmt.Printf("%s complete: %d %s, %d skipped, %d failed\n", label, synced.Load(), word, skipped.Load(), failed.Load())
	if failed.Load() > 0 {
		return fmt.Errorf("%s completed with %d errors", action, failed.Load())
	}
	return nil
}

func (m *Manager) List(targetNames []string, includeArchived bool, workers int) error {
	targets, err := m.targetsFor(targetNames)
	if err != nil {
		return err
	}
	probe := &historyProbe{}
	defer probe.close()
	for _, t := range targets {
		fmt.Printf("Target: %s (%s/%s) path=%s\n", t.Name, t.Provider, t.Org, t.Path)
		var jobs []statusJob
		orgs := []orgKey{{t.Provider, t.Org}}
		if t.Repo == "" {
			entries, err := os.ReadDir(t.Path)
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			for _, entry := range entries {
				dir := filepath.Join(t.Path, entry.Name())
				if entry.IsDir() && isGitRepo(dir) {
					jobs = append(jobs, statusJob{path: dir, target: t.Name, provider: t.Provider, org: t.Org, name: entry.Name()})
				}
			}
		} else {
			var err error
			jobs, orgs, err = discoverStatusJobs([]config.Target{t}, m.config)
			if err != nil {
				return err
			}
		}
		index, metadataErrors := m.buildRepoIndex(orgs, nil)
		local := pool.Run(jobs, workers, func(job statusJob) RepoStatus {
			job.token = m.config.Providers[t.Provider].Token
			if err := metadataErrors[orgKey{job.provider, job.org}.string()]; err != nil {
				return RepoStatus{Name: job.name, Path: job.path, Error: err.Error()}
			}
			return m.inspectListedIdentity(job, index[orgKey{job.provider, job.org}.string()], probe)
		})
		sort.Slice(local, func(i, j int) bool { return local[i].Path < local[j].Path })
		if t.Repo == "" {
			if err := metadataErrors[orgKey{t.Provider, t.Org}.string()]; err != nil {
				fmt.Printf("  [ERROR] listing org: %v\n", err)
			}
			var names []string
			for name := range index[orgKey{t.Provider, t.Org}.string()] {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				r := index[orgKey{t.Provider, t.Org}.string()][name]
				mark := "[ ]"
				var flags []string
				if r.Archived {
					flags = append(flags, "archived")
				}
				attention := false
				for _, s := range local {
					if s.RepositoryID == r.ID && !hasStatusError(s) {
						mark = "[x]"
						if s.IdentityIssue != "" {
							flags = append(flags, "at "+s.Name+": "+s.IdentityIssue)
							attention = true
						}
					} else if s.Name == name {
						flags = append(flags, "local path occupied; "+s.IdentityIssue+statusErrorMessage(s))
						attention = true
					}
				}
				if r.Archived && !includeArchived && !attention {
					continue
				}
				fmt.Printf("  %s %s", mark, name)
				if len(flags) > 0 {
					fmt.Printf(" (%s)", strings.Join(flags, "; "))
				}
				fmt.Println()
			}
			for _, s := range local {
				if _, named := index[orgKey{t.Provider, t.Org}.string()][s.Name]; !named && (s.RepositoryID == 0 || hasStatusError(s) || s.Orphan) {
					fmt.Printf("  [x] %s (orphan; %s%s)\n", s.Name, s.IdentityIssue, statusErrorMessage(s))
				}
			}
		} else {
			for _, s := range local {
				mark := "[ ]"
				if !s.Missing && s.RepositoryID != 0 && s.RemoteName == s.Name && s.IdentityIssue == "" && !hasStatusError(s) {
					mark = "[x]"
				}
				fmt.Printf("  %s %s -> %s", mark, s.Name, s.Path)
				if s.IdentityIssue != "" || hasStatusError(s) {
					fmt.Printf(" (%s%s)", s.IdentityIssue, statusErrorMessage(s))
				}
				fmt.Println()
			}
		}
		fmt.Println()
	}
	return nil
}

func (m *Manager) scanStatus(job statusJob, metadata *scanMetadata, probe *historyProbe, progress *progressReporter, failure error) statusResult {
	defer progress.checkFinished(job.path)
	var timing RepoTiming
	if job.missing {
		s := m.inspectIdentity(job, metadata.index[orgKey{job.provider, job.org}.string()], probe)
		if err := metadata.errors[orgKey{job.provider, job.org}.string()]; err != nil {
			s.MetadataError = err.Error()
		}
		if failure != nil {
			s.Error, s.ReconcileError = failure.Error(), true
		}
		if progress != nil {
			s.Removed = progress.removed[job.path]
		}
		return statusResult{status: s}
	}
	key := orgKey{job.provider, job.org}.string()
	identity := RepoStatus{Path: job.path, Target: job.target, Provider: job.provider, Org: job.org, Name: job.name}
	if metadataErr := metadata.errors[key]; metadataErr != nil {
		identity.MetadataError = metadataErr.Error()
	} else {
		if metadata.readOnly {
			identity = m.inspectReadOnlyIdentity(job, metadata.index[key], probe, true)
		} else {
			identity = m.inspectIdentity(job, metadata.index[key], probe)
		}
	}
	if failure != nil {
		identity.Error, identity.ReconcileError = failure.Error(), true
	}
	fetch := !hasStatusError(identity) && identity.IdentityIssue == "" && !identity.Orphan
	if fetch {
		var err error
		if progress == nil {
			err = m.confirmStatusIdentity(identity)
		} else {
			err = m.saveVerifiedIdentity(identity)
		}
		if err != nil {
			identity.Error = err.Error()
			fetch = false
		}
	}
	status := getRepoStatusWithProgressAndFetch(job.path, job.target, job.org, job.name, job.provider, job.token, &timing, progress, fetch)
	if fetch && status.RemoteError == "" {
		if err := m.confirmStatusIdentity(identity); err != nil {
			identity.Error = err.Error()
		}
	}
	status.IdentityIssue, status.RepositoryID, status.RemoteName = identity.IdentityIssue, identity.RepositoryID, identity.RemoteName
	status.ReplacementID, status.identity, status.repository = identity.ReplacementID, identity.identity, identity.repository
	status.gitDirInfo = identity.gitDirInfo
	status.ReconcileError, status.Orphan, status.MetadataError = identity.ReconcileError, identity.Orphan, identity.MetadataError
	status.Transferred = identity.Transferred
	if identity.Error != "" {
		status.Error = identity.Error
	}
	if identity.repository != nil {
		applyRemoteState(&status, *identity.repository)
	}
	return statusResult{status: status, timing: timing}
}
