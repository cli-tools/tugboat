package repo

import (
	"fmt"
	"io"
	"strings"
	"sync"
)

// progressReporter writes complete lines immediately, including from scan workers.
// A nil reporter keeps read-only commands' output unchanged.
type progressReporter struct {
	mu        sync.Mutex
	out       io.Writer
	verbose   bool
	total     int
	checked   int
	completed int
	notes     map[string][]string
	removed   map[string]bool
	planned   map[string]bool
	finished  map[string]bool
}

func (p *progressReporter) printf(format string, args ...any) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	fmt.Fprintf(p.out, format, args...)
}

func (p *progressReporter) beginChecks(total int) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.total = total
	if p.verbose {
		fmt.Fprintf(p.out, "Checking %d repositories...\n", total)
	}
}

func (p *progressReporter) checkFinished(path string) {
	if p == nil || !p.verbose {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.checked++
	p.planLocked(path)
	fmt.Fprintf(p.out, "  [%*d/%d] Checked %s\n", len(fmt.Sprint(p.total)), p.checked, p.total, path)
}

// finish records an update outcome, independently of scan completion.
func (p *progressReporter) finish(format string, args ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.completed++
	line := strings.TrimSpace(fmt.Sprintf(format, args...))
	line = strings.ReplaceAll(line, "\n", "; ")
	if len(args) > 0 {
		if path, ok := args[0].(string); ok {
			p.planLocked(path)
			if p.planned != nil {
				p.finished[path] = true
			}
			if len(p.notes[path]) > 0 {
				line += "; " + strings.Join(p.notes[path], "; ")
			}
		}
	}
	fmt.Fprintf(p.out, "  [%*d/%d] %s\n", len(fmt.Sprint(p.total)), p.completed, p.total, line)
}

func (p *progressReporter) note(path, message string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.notes == nil {
		p.notes = make(map[string][]string)
	}
	p.notes[path] = append(p.notes[path], message)
}

// detail reports activity only when explicitly requested.
func (p *progressReporter) detail(format string, args ...any) {
	if p == nil || !p.verbose {
		return
	}
	p.printf(format, args...)
}

// Count known checkout paths without waiting for identity or Git operations.
// New foldouts can extend the plan, while a pure rename merges two paths.
func (p *progressReporter) beginPlan(paths []string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.planned = make(map[string]bool)
	p.finished = make(map[string]bool)
	p.total = 0
	for _, path := range paths {
		p.planLocked(path)
	}
	if p.verbose {
		fmt.Fprintf(p.out, "Checking %d repositories...\n", p.total)
	}
}

func (p *progressReporter) planLocked(path string) {
	if p.planned != nil && !p.planned[path] {
		p.planned[path] = true
		p.total++
	}
}

func (p *progressReporter) plan(paths ...string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, path := range paths {
		p.planLocked(path)
	}
}

func (p *progressReporter) forget(path string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.forgetLocked(path)
}

func (p *progressReporter) forgetLocked(path string) {
	if p.planned[path] && !p.finished[path] {
		delete(p.planned, path)
		p.total--
	}
}

func (p *progressReporter) relocate(from, to string) {
	if p == nil || from == to {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.forgetLocked(from)
	p.planLocked(to)
}
