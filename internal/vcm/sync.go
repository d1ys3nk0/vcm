package vcm

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

type syncIntent struct {
	Version     int    `json:"version"`
	TrunkBefore string `json:"trunk_before"`
	Repository  string `json:"repository"`
	Path        string `json:"path"`
	Before      string `json:"before"`
	Target      string `json:"target"`
	Operation   string `json:"operation"`
}

func (e *Engine) syncIntent(r Repository, p, target, operation string) (func() error, error) {
	filename := filepath.Join(e.store.dir, r.Name+".sync")
	if err := secureDirectory(e.store.dir, true); err != nil {
		return nil, err
	}
	current, err := head(p)
	if err != nil {
		return nil, err
	}
	trunk, err := git(p, "rev-parse", "refs/heads/"+r.Trunk)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(syncIntent{Version: 1, TrunkBefore: trunk, Repository: r.Name, Path: p, Before: current, Target: target, Operation: operation})
	if err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(e.store.dir, ".sync-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err == nil {
		err = ce
	}
	if err != nil {
		return nil, err
	}
	if err = os.Rename(f.Name(), filename); err != nil {
		return nil, err
	}
	if err = syncDirectory(e.store.dir); err != nil {
		return nil, err
	}
	return func() error {
		if err := os.Remove(filename); err != nil {
			return err
		}
		return syncDirectory(e.store.dir)
	}, nil
}

func (e *Engine) reconcileSync(r Repository, p string) error {
	filename := filepath.Join(e.store.dir, r.Name+".sync")
	old, err := readSync(filename)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if old.Path != p || old.Repository != r.Name {
		return fmt.Errorf("repository %s: synchronization ownership mismatch", r.Name)
	}
	current, err := head(p)
	if err != nil {
		return err
	}
	if current != old.Before && current != old.Target && current != old.TrunkBefore {
		return fmt.Errorf("repository %s: interrupted synchronization has ambiguous revision; inspect %s and restore recorded before/target before retry", r.Name, filename)
	}
	if err = clean(p); err != nil {
		return fmt.Errorf("repository %s: interrupted synchronization needs repair: %w", r.Name, err)
	}
	if err = os.Remove(filename); err != nil {
		return err
	}
	return syncDirectory(e.store.dir)
}

func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func readSync(path string) (syncIntent, error) {
	var intent syncIntent
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return intent, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return intent, err
	}
	if !info.Mode().IsRegular() || !ownerOnly(info) || info.Mode().Perm() != 0600 {
		return intent, fmt.Errorf("insecure synchronization intent %s", path)
	}
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if err = d.Decode(&intent); err != nil {
		return intent, err
	}
	var extra any
	if err = d.Decode(&extra); err != io.EOF {
		return intent, fmt.Errorf("invalid synchronization intent trailing data")
	}
	if intent.Version != 1 || !objectPattern.MatchString(intent.Before) || !objectPattern.MatchString(intent.Target) || !objectPattern.MatchString(intent.TrunkBefore) {
		return intent, fmt.Errorf("invalid synchronization intent")
	}
	return intent, nil
}
