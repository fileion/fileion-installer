package run

import (
	"context"
	"os"
	"sync"

	"fileion-installer/internal/manifest"
)

// Ahead downloads the apps that come next while the current one installs, so the person is not waiting for each download in turn. Installs stay strictly
// one at a time (the caller's loop); only downloads overlap: at most MaxAhead at once, and only the next Window apps after the one being run, in order.
// Stop ends every download in progress (the runner's own StopCh watcher, per download); Close ends the rest and deletes the files nobody took, so a
// stop, a failure or the end of the run leaves nothing in the temp folder.
type Ahead struct {
	r      *Runner
	ctx    context.Context
	cancel context.CancelFunc
	apps   []manifest.App
	sem    chan struct{}
	mu     sync.Mutex
	jobs   map[int]*job
	cur    int
}

type job struct {
	done chan struct{}
	f    Fetched
}

const (
	MaxAhead = 2 // downloads at once
	Window   = 3 // the current app and the next two
)

func (r *Runner) NewAhead(ctx context.Context, apps []manifest.App) *Ahead {
	c, cancel := context.WithCancel(ctx)
	return &Ahead{r: r, ctx: c, cancel: cancel, apps: apps, sem: make(chan struct{}, MaxAhead), jobs: map[int]*job{}}
}

// Fill starts the downloads of the apps from index i on (the current one and the next two) that need one and want says are part of this pass.
func (a *Ahead) Fill(i int, want func(int) bool) {
	for j := i; j < len(a.apps) && j < i+Window; j++ {
		if !want(j) {
			continue
		}
		a.mu.Lock()
		_, started := a.jobs[j]
		a.mu.Unlock()
		if started {
			continue
		}
		if _, ends := a.r.pre(a.ctx, a.apps[j]); ends || a.apps[j].FlatpakID != "" {
			continue
		}
		jb := &job{done: make(chan struct{})}
		a.mu.Lock()
		a.jobs[j] = jb
		a.mu.Unlock()
		go func(j int, jb *job) {
			defer close(jb.done)
			select {
			case a.sem <- struct{}{}:
				defer func() { <-a.sem }()
			case <-a.ctx.Done():
				jb.f = Fetched{Err: a.ctx.Err()}
				return
			}
			jb.f = a.r.fetchApp(a.ctx, a.apps[j])
			if jb.f.Err != nil && a.r.Stopped != nil && a.r.Stopped() { // Stop ended it: its row says so now, not when its turn comes
				a.r.say(a.apps[j], NotRun, "Not run", "")
			} else if jb.f.Err == nil && !a.isCurrent(j) {
				a.r.say(a.apps[j], Queued, "Downloaded, waiting for its turn", "")
			}
		}(j, jb)
	}
}

// current is the index being run (set by Take); a download that ends before its turn shows "waiting".
func (a *Ahead) isCurrent(j int) bool { a.mu.Lock(); defer a.mu.Unlock(); return a.cur == j }

// Take gives the download of app i (nil when none was started: the runner then downloads it itself).
func (a *Ahead) Take(i int) func() Fetched {
	a.mu.Lock()
	a.cur = i
	jb := a.jobs[i]
	delete(a.jobs, i)
	a.mu.Unlock()
	if jb == nil {
		return nil
	}
	return func() Fetched { <-jb.done; return jb.f }
}

// Close ends what is still downloading and deletes every file nobody took.
func (a *Ahead) Close() {
	a.cancel()
	a.mu.Lock()
	jobs := a.jobs
	a.jobs = map[int]*job{}
	a.mu.Unlock()
	for _, jb := range jobs {
		<-jb.done
		if jb.f.File != "" {
			os.Remove(jb.f.File)
		}
	}
}
