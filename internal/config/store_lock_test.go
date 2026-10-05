package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestConfigLockContention(t *testing.T) {
	p := storePaths(t)
	first, err := acquireConfigLock(context.Background(), p, true, true)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		close(started)
		lock, e := acquireConfigLock(ctx, p, true, true)
		if lock != nil {
			_ = releaseConfigLock(lock)
		}
		result <- e
	}()
	<-started
	cancel()
	if e := <-result; !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if err := releaseConfigLock(first); err != nil {
		t.Fatal(err)
	}
	next, err := acquireConfigLock(context.Background(), p, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := releaseConfigLock(next); err != nil {
		t.Fatal(err)
	}
}

func TestConfigLockIdentity(t *testing.T) {
	p := storePaths(t)
	if err := os.MkdirAll(p.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(filepath.Dir(p.ConfigDir), "alias")
	if err := os.Symlink(p.ConfigDir, alias); err != nil {
		t.Fatal(err)
	}
	other := p
	other.ConfigDir = alias
	a, e := acquireConfigLock(context.Background(), p, true, true)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, e := acquireConfigLock(ctx, other, true, true); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal(e)
	}
	if e := releaseConfigLock(a); e != nil {
		t.Fatal(e)
	}
	b, e := acquireConfigLock(context.Background(), other, true, true)
	if e != nil {
		t.Fatal(e)
	}
	defer releaseConfigLock(b)
	if a.Name() != b.Name() {
		t.Fatal(a.Name(), b.Name())
	}
	distinct := p
	distinct.ConfigDir += "-different"
	c, e := acquireConfigLock(context.Background(), distinct, true, true)
	if e != nil {
		t.Fatal(e)
	}
	defer releaseConfigLock(c)
	bs, err := b.Stat()
	if err != nil {
		t.Fatal(err)
	}
	cs, err := c.Stat()
	if err != nil || os.SameFile(bs, cs) {
		t.Fatal("distinct config directories share a lock", err)
	}
}

func TestReadLockNoCreation(t *testing.T) {
	p := storePaths(t)
	if _, e := acquireConfigLock(context.Background(), p, false, false); !errors.Is(e, os.ErrNotExist) {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Dir(p.ConfigDir)); !errors.Is(e, os.ErrNotExist) {
		t.Fatal(e)
	}
	dir, e := openConfigDir(p.ConfigDir, true)
	if e != nil {
		t.Fatal(e)
	}
	_ = dir.Close()
	if _, e := acquireConfigLock(context.Background(), p, false, false); !errors.Is(e, os.ErrNotExist) {
		t.Fatal(e)
	}
	entries, _ := os.ReadDir(p.ConfigDir)
	if len(entries) != 0 {
		t.Fatal(entries)
	}
}

func TestStoreLockSharedAcrossStateRoots(t *testing.T) {
	p := storePaths(t)
	seedStore(t, p, 0, false)
	other := p
	other.StateDir = p.StateDir + "-other"
	entered, release := make(chan struct{}), make(chan struct{})
	first := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		_, err := NewStore(p).Update(ctx, 0, func(s *State) error {
			close(entered)
			<-release
			c := s.Personal.Connections["paper"]
			c.Label = "first"
			s.Personal.Connections["paper"] = c
			return nil
		})
		first <- err
	}()
	select {
	case <-entered:
	case err := <-first:
		t.Fatal(err)
	}
	second := make(chan error, 1)
	go func() {
		_, err := NewStore(other).Update(ctx, 0, func(s *State) error {
			c := s.Personal.Connections["paper"]
			c.Description = "second"
			s.Personal.Connections["paper"] = c
			return nil
		})
		second <- err
	}()
	var secondErr error
	completed := false
	select {
	case secondErr = <-second:
		completed = true
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if !completed {
		secondErr = <-second
	}
	if !errors.Is(secondErr, ErrConfigConflict) {
		t.Fatalf("second writer must observe new revision, got %v", secondErr)
	}
	s, err := NewStore(other).Read(ctx)
	if err != nil || s.Selections.Revision != 1 || s.Personal.Connections["paper"].Label != "first" {
		t.Fatal(s, err)
	}
	info, err := os.Lstat(filepath.Join(p.ConfigDir, ".mcparcel.lock"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal(info, err)
	}
	if _, err := os.Stat(other.StateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("config lock created state dir: %v", err)
	}
}
