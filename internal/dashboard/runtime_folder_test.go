package dashboard

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestCloseCancelsFolderOpen(t *testing.T) {
	root := t.TempDir()
	runtime := NewRuntime(DefaultSettings(), filepath.Join(root, "settings.json"), root)
	started := make(chan struct{})
	runtime.openFolder = func(ctx context.Context, path string) error {
		if path != filepath.Join(root, "recordings") {
			t.Errorf("open folder path = %q, want recordings directory", path)
		}
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}

	runtime.OpenFolder()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("folder opener did not start")
	}

	closed := make(chan error, 1)
	go func() { closed <- runtime.Close() }()
	select {
	case err := <-closed:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Close() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close waited for a canceled folder opener")
	}
}

func TestCloseWaitsForAdmittedActionAndRejectsLaterWork(t *testing.T) {
	root := t.TempDir()
	runtime := NewRuntime(DefaultSettings(), filepath.Join(root, "settings.json"), root)
	if !runtime.beginAction() {
		t.Fatal("new runtime rejected an action")
	}

	closed := make(chan error, 1)
	go func() { closed <- runtime.Close() }()
	deadline := time.Now().Add(time.Second)
	for !runtime.closed.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !runtime.closed.Load() {
		t.Fatal("Close did not shut the action gate")
	}
	select {
	case err := <-closed:
		t.Fatalf("Close returned before admitted action finished: %v", err)
	default:
	}
	runtime.wg.Done()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not finish after admitted action")
	}
	if runtime.beginAction() {
		runtime.wg.Done()
		t.Fatal("closed runtime admitted new asynchronous work")
	}

	var opened atomic.Bool
	runtime.openFolder = func(context.Context, string) error {
		opened.Store(true)
		return nil
	}
	runtime.OpenFolder()
	if opened.Load() {
		t.Fatal("OpenFolder ran after Runtime.Close")
	}
}
