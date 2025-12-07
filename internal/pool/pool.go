package pool

import (
	"runtime"
	"sync"
)

// Run executes tasks in parallel using a worker pool.
// If workers <= 0, defaults to runtime.GOMAXPROCS(0) (typically number of CPU cores).
// Results are returned in non-deterministic order.
func Run[T, R any](items []T, workers int, fn func(T) R) []R {
	if len(items) == 0 {
		return nil
	}

	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	if workers > len(items) {
		workers = len(items)
	}

	jobs := make(chan T, len(items))
	results := make(chan R, len(items))

	// Start workers
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range jobs {
				results <- fn(item)
			}
		}()
	}

	// Send jobs
	for _, item := range items {
		jobs <- item
	}
	close(jobs)

	// Close results when workers finish
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results
	out := make([]R, 0, len(items))
	for r := range results {
		out = append(out, r)
	}
	return out
}
