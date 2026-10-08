package repo

import (
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

type localWorkRef struct {
	ref   string
	label string
}

type unpublishedWork struct {
	count int
	refs  map[string]int
}

func localWorkLabel(ref string) string {
	switch {
	case strings.HasPrefix(ref, "refs/heads/"):
		return "branch " + strings.TrimPrefix(ref, "refs/heads/")
	case strings.HasPrefix(ref, "refs/tags/"):
		return "tag " + strings.TrimPrefix(ref, "refs/tags/")
	case ref == "refs/stash":
		return "stash"
	default:
		return ref
	}
}

func checkoutHeadLabel(dir string) string {
	branch, err := gitOutput(dir, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err == nil {
		return "branch " + strings.TrimSpace(branch)
	}
	return "detached HEAD"
}

// Count unique unpublished commits first. Only blocked cleanups inspect each
// ref, so normal up-to-date checkouts do not pay for branch diagnostics.
func inspectUnpublishedWork(dir string, refs []localWorkRef, remoteObjects []string) (unpublishedWork, error) {
	var work unpublishedWork
	if len(refs) == 0 {
		return work, nil
	}
	var exclusions strings.Builder
	for _, oid := range remoteObjects {
		fmt.Fprintf(&exclusions, "^%s\n", oid)
	}
	count := func(names []string) (int, error) {
		args := append([]string{"rev-list", "--count", "--stdin"}, names...)
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = gitEnvNoPrompt()
		cmd.Stdin = strings.NewReader(exclusions.String())
		output, err := cmd.CombinedOutput()
		if err != nil {
			return 0, fmt.Errorf("checking unpublished commits: %w: %s", err, strings.TrimSpace(string(output)))
		}
		return strconv.Atoi(strings.TrimSpace(string(output)))
	}
	names := make([]string, 0, len(refs))
	for _, ref := range refs {
		names = append(names, ref.ref)
	}
	var err error
	work.count, err = count(names)
	if err != nil || work.count == 0 {
		return work, err
	}
	work.refs = make(map[string]int)
	for _, ref := range refs {
		n, err := count([]string{ref.ref})
		if err != nil {
			return work, err
		}
		if n > work.refs[ref.label] {
			work.refs[ref.label] = n
		}
	}
	return work, nil
}

func (w unpublishedWork) reason() string {
	labels := make([]string, 0, len(w.refs))
	for label := range w.refs {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	for i, label := range labels {
		labels[i] = fmt.Sprintf("%s (%d)", label, w.refs[label])
	}
	return "unpublished commits: " + strings.Join(labels, ", ")
}
