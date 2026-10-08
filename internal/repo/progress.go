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
		if path, ok := args[0].(string); ok && len(p.notes[path]) > 0 {
			line += "; " + strings.Join(p.notes[path], "; ")
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
