package auth

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestOAuthLockFile(t *testing.T) {
	dir := healthDir(t)
	if set, err := ReadOAuthLock(dir); err != nil || len(set) != 0 {
		t.Fatal("missing file", set, err)
	}
	if err := WriteOAuthLock(dir, []string{"local:b", "local:a", "local:b"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, oauthLockFile))
	if err != nil || string(b) != `{"v":1,"connections":["local:a","local:b"]}` {
		t.Fatal(string(b), err)
	}
	if st, _ := os.Stat(filepath.Join(dir, oauthLockFile)); st.Mode().Perm() != 0o600 {
		t.Fatal(st.Mode())
	}
	if err := WriteOAuthLock(dir, []string{"local:b"}); err != nil {
		t.Fatal(err)
	}
	if set, err := ReadOAuthLock(dir); err != nil || !reflect.DeepEqual(set, map[string]bool{"local:b": true}) {
		t.Fatal(set, err)
	}
	if set, err := ReadOAuthLock(filepath.Join(dir, "absent")); err != nil || len(set) != 0 {
		t.Fatal("missing directory", set, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatal("temp files left", len(entries))
	}
}

func TestOAuthLockFileRejectsInvalid(t *testing.T) {
	for name, content := range map[string]string{"json": "{", "version": `{"v":2,"connections":[]}`} {
		t.Run(name, func(t *testing.T) {
			dir := healthDir(t)
			if err := os.WriteFile(filepath.Join(dir, oauthLockFile), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadOAuthLock(dir); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}
