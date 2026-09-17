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

// Legacy create synchronization journals are preserved for the previous binary.
func (e *Engine) reconcileSync(r Repository, p string) error {
	filename := filepath.Join(e.store.dir, r.Name+".sync")
	if _, err := os.Lstat(filename); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	return fmt.Errorf("repository %s: legacy synchronization journal %s; use the previous VCM binary to finish or discard the interrupted creation", r.Name, filename)
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
