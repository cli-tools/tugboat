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
	total     int
	checked   int
	completed int
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
	fmt.Fprintf(p.out, "Checking %d repositories...\n", total)
}

func (p *progressReporter) checkFinished(path string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.checked++
	fmt.Fprintf(p.out, "  [%d/%d] Checked %s\n", p.checked, p.total, path)
}

// finish records an update outcome, independently of scan completion.
func (p *progressReporter) finish(format string, args ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.completed++
	fmt.Fprintf(p.out, "  [%d/%d] %s", p.completed, p.total, strings.TrimLeft(fmt.Sprintf(format, args...), " "))
}
