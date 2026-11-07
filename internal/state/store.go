package state

// Store is the state a sync keeps up to date: the State loaded from a state
// file, which Save writes back to the same file. A sync calls Save after
// every step, so it never forgets something it did, even if it's
// interrupted. Like the State, a Store is safe for concurrent use.
type Store struct {
	*State

	path string
	// saved is what the file holds, as far as the Store knows. It's
	// guarded by State.mu.
	saved []byte
}

// Open loads the state file at path, as Load does, to keep it up to date.
func Open(path string) (*Store, error) {
	s, data, err := load(path, FormatVersion, migrations)
	if err != nil {
		return nil, err
	}
	return &Store{State: s, path: path, saved: data}, nil
}

// Path returns the state file's path.
func (s *Store) Path() string {
	return s.path
}

// Save writes the state back to the state file, as State.Save does. It only
// writes when the state changed since it was loaded or last saved, so it's
// cheap to call after every step. Concurrent calls are written in turn.
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.FormatVersion = FormatVersion
	data, err := s.writeLocked(s.path, s.saved)
	if err != nil {
		return err
	}
	s.saved = data
	return nil
}
