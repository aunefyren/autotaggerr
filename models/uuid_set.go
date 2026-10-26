package models

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/google/uuid"
)

// UUIDSet is a set of IDs kept in one text column as a JSON array — for a handful of
// libraries on an artist row, where a join table would cost a query on every read of
// something nearly every collection page reads. Sorted and de-duplicated on write, so
// one set always stores the same way. Empty stores as "[]", and NULL reads as empty.
type UUIDSet []uuid.UUID

// Value implements driver.Valuer.
func (s UUIDSet) Value() (driver.Value, error) {
	b, err := json.Marshal(s.normalized())
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

// Scan implements sql.Scanner.
func (s *UUIDSet) Scan(src any) error {
	var raw []byte
	switch v := src.(type) {
	case nil:
		*s = nil
		return nil
	case string:
		raw = []byte(v)
	case []byte:
		raw = v
	default:
		return fmt.Errorf("UUIDSet: cannot scan %T", src)
	}
	if len(raw) == 0 {
		*s = nil
		return nil
	}
	var ids []uuid.UUID
	if err := json.Unmarshal(raw, &ids); err != nil {
		return err
	}
	*s = UUIDSet(ids).normalized()
	return nil
}

// Contains reports whether id is in the set.
func (s UUIDSet) Contains(id uuid.UUID) bool {
	for _, x := range s {
		if x == id {
			return true
		}
	}
	return false
}

// With is the set with id added.
func (s UUIDSet) With(id uuid.UUID) UUIDSet {
	if id == uuid.Nil || s.Contains(id) {
		return s.normalized()
	}
	return append(append(UUIDSet{}, s...), id).normalized()
}

// Without is the set with id removed.
func (s UUIDSet) Without(id uuid.UUID) UUIDSet {
	out := UUIDSet{}
	for _, x := range s {
		if x != id {
			out = append(out, x)
		}
	}
	return out.normalized()
}

// normalized drops uuid.Nil and duplicates and sorts, never returning nil.
func (s UUIDSet) normalized() UUIDSet {
	seen := map[uuid.UUID]bool{}
	out := UUIDSet{}
	for _, x := range s {
		if x != uuid.Nil && !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}
