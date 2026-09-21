package service

import "messagecatalog/internal/store"

func openStore(dir string) (*store.Store, error) { return store.Open(dir) }

func (s *Service) CommitForTest(fn func() error) error {
	return s.Store.Commit(func(st *store.State) error { return fn() })
}
