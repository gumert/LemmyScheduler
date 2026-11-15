package migrations

import (
	"errors"
	"io"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"testing"

	"github.com/golang-migrate/migrate/v4/source/iofs"
)

// fileName mirrors the golang-migrate rule: {version}_{title}.{up|down}.sql.
var fileName = regexp.MustCompile(`^([0-9]+)_(.+)\.(up|down)\.sql$`)

// versions returns, per migration version, which directions exist.
func versions(t *testing.T) map[uint64]map[string]bool {
	t.Helper()
	entries, err := fs.ReadDir(FS, ".")
	if err != nil {
		t.Fatalf("read embedded FS: %v", err)
	}
	found := map[uint64]map[string]bool{}
	for _, e := range entries {
		m := fileName.FindStringSubmatch(e.Name())
		if m == nil {
			t.Fatalf("%q does not match {version}_{title}.{up|down}.sql", e.Name())
		}
		v, err := strconv.ParseUint(m[1], 10, 64)
		if err != nil {
			t.Fatalf("%q: bad version: %v", e.Name(), err)
		}
		if found[v] == nil {
			found[v] = map[string]bool{}
		}
		if found[v][m[3]] {
			t.Fatalf("version %d has more than one %s file", v, m[3])
		}
		found[v][m[3]] = true
	}
	return found
}

func TestEveryUpHasMatchingDown(t *testing.T) {
	found := versions(t)
	if len(found) == 0 {
		t.Fatal("no migrations embedded")
	}
	for v, dirs := range found {
		if !dirs["up"] || !dirs["down"] {
			t.Errorf("version %d needs both an up and a down file, has %v", v, dirs)
		}
	}
}

func TestVersionsAreContiguousFromOne(t *testing.T) {
	found := versions(t)
	var vs []uint64
	for v := range found {
		vs = append(vs, v)
	}
	sort.Slice(vs, func(i, j int) bool { return vs[i] < vs[j] })
	for i, v := range vs {
		if want := uint64(i + 1); v != want {
			t.Fatalf("versions = %v, want contiguous from 1 (position %d is %d, want %d)", vs, i, v, want)
		}
	}
}

// TestIofsParsesEmbeddedFS validates golang-migrate's own filename rules, and
// that the files it walks are exactly the ones this package expects.
func TestIofsParsesEmbeddedFS(t *testing.T) {
	src, err := iofs.New(FS, ".")
	if err != nil {
		t.Fatalf("iofs.New: %v", err)
	}
	defer src.Close()

	want := len(versions(t))
	got := 0
	v, err := src.First()
	for err == nil {
		got++
		r, _, openErr := src.ReadUp(v)
		if openErr != nil {
			t.Fatalf("ReadUp(%d): %v", v, openErr)
		}
		if body, _ := io.ReadAll(r); len(body) == 0 {
			t.Errorf("version %d up migration is empty", v)
		}
		r.Close()
		v, err = src.Next(v)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("walking migrations ended with %v, want fs.ErrNotExist", err)
	}
	if got != want {
		t.Fatalf("iofs saw %d migrations, want %d", got, want)
	}
}
