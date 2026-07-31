package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/moddengine/whmcs-container-controller/internal/model"
	"github.com/pelletier/go-toml/v2"
)

var ErrNotFound = errors.New("service not found")

type Repository struct {
	servicesDir   string
	tombstonesDir string
	// ponytail: one lock is enough for an MVP controller; use keyed locks if write contention becomes measurable.
	mu sync.Mutex
}

func New(servicesDir, tombstonesDir string) *Repository {
	return &Repository{servicesDir: servicesDir, tombstonesDir: tombstonesDir}
}

func (r *Repository) Init() error {
	for _, dir := range []string{r.servicesDir, r.tombstonesDir} {
		if err := os.MkdirAll(dir, 0750); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) Get(id string) (model.Service, error) {
	var service model.Service
	b, err := os.ReadFile(r.servicePath(id))
	if errors.Is(err, os.ErrNotExist) {
		return service, ErrNotFound
	}
	if err != nil {
		return service, err
	}
	if err := toml.Unmarshal(b, &service); err != nil {
		return service, fmt.Errorf("parse service %s: %w", id, err)
	}
	return service, nil
}

func (r *Repository) Put(service model.Service) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, err := toml.Marshal(service)
	if err != nil {
		return err
	}
	return atomicWrite(r.servicePath(service.ID), b, true)
}

func (r *Repository) DeleteLive(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	err := os.Remove(r.servicePath(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (r *Repository) PutTombstone(t model.Tombstone) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, err := toml.Marshal(t)
	if err != nil {
		return err
	}
	return atomicWrite(r.tombstonePath(t.ID), b, false)
}

func (r *Repository) GetTombstone(id string) (model.Tombstone, error) {
	var tombstone model.Tombstone
	b, err := os.ReadFile(r.tombstonePath(id))
	if errors.Is(err, os.ErrNotExist) {
		return tombstone, ErrNotFound
	}
	if err != nil {
		return tombstone, err
	}
	return tombstone, toml.Unmarshal(b, &tombstone)
}

func (r *Repository) List() ([]model.Service, error) {
	entries, err := os.ReadDir(r.servicesDir)
	if err != nil {
		return nil, err
	}
	services := make([]model.Service, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".toml") {
			continue
		}
		service, err := r.Get(strings.TrimSuffix(entry.Name(), ".toml"))
		if err != nil {
			return nil, err
		}
		services = append(services, service)
	}
	sort.Slice(services, func(i, j int) bool { return services[i].ID < services[j].ID })
	return services, nil
}

func (r *Repository) TombstoneCount() (int, error) {
	entries, err := os.ReadDir(r.tombstonesDir)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".toml") {
			count++
		}
	}
	return count, nil
}

func (r *Repository) servicePath(id string) string { return filepath.Join(r.servicesDir, id+".toml") }
func (r *Repository) tombstonePath(id string) string {
	return filepath.Join(r.tombstonesDir, id+".toml")
}

func atomicWrite(path string, content []byte, backup bool) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0640); err == nil {
		_, err = f.Write(content)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if backup {
		if old, readErr := os.ReadFile(path); readErr == nil {
			if err := os.WriteFile(path+".bak", old, 0640); err != nil {
				return err
			}
		}
	}
	return os.Rename(tmp, path)
}
