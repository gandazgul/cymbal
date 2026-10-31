package index

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestIndexFormat3RefreshesReExportImports(t *testing.T) {
	for _, ext := range []string{"js", "ts", "tsx"} {
		t.Run(ext, func(t *testing.T) {
			repo, db := t.TempDir(), filepath.Join(t.TempDir(), "index.db")
			t.Cleanup(CloseAll)
			repo = canonicalPath(repo)
			path := filepath.Join(repo, "barrel."+ext)
			src := "import './setup';\nexport { value } from './named';\nexport * from './all';\n"
			if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
				t.Fatal(err)
			}
			if stats, err := Index(repo, db, Options{Workers: 1}); err != nil || stats.Errors != 0 || stats.FilesIndexed != 1 {
				t.Fatalf("initial index: %+v, %v", stats, err)
			}
			store, err := OpenStore(db)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			want := []string{"./all", "./named", "./setup"}
			assertImports := func() {
				t.Helper()
				if got, err := store.FileImports(path); err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("barrel imports = %v, %v; want %v", got, err, want)
				}
			}
			assertImports()

			// Format 2 kept ordinary imports, but not static re-export edges.
			if _, err := store.db.Exec(`DELETE FROM imports WHERE raw_path != './setup'`); err != nil {
				t.Fatal(err)
			}
			if err := store.SetMeta(metaIndexFormat, "2"); err != nil {
				t.Fatal(err)
			}
			if stats, err := Index(repo, db, Options{Workers: 1}); err != nil || stats.Errors != 0 || stats.FilesIndexed != 1 || stats.FilesSkipped != 0 {
				t.Fatalf("format 2 refresh: %+v, %v; want unchanged barrel reparsed", stats, err)
			}
			assertImports()
			if got, err := store.GetMeta(metaIndexFormat); err != nil || got != indexFormat {
				t.Fatalf("format = %q, %v; want %q", got, err, indexFormat)
			}
			if stats, err := Index(repo, db, Options{Workers: 1}); err != nil || stats.Errors != 0 || stats.FilesIndexed != 0 || stats.FilesSkipped != 1 {
				t.Fatalf("after upgrade: %+v, %v; want unchanged barrel skipped", stats, err)
			}
			assertImports()
		})
	}
}

func TestIndexFormatUpgradeRetriesFailedWrites(t *testing.T) {
	repo, db := freshnessFixture(t)
	t.Cleanup(CloseAll)
	store, err := OpenStore(db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SetMeta(metaIndexFormat, "2"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER fail_upgrade BEFORE INSERT ON symbols BEGIN SELECT RAISE(FAIL, 'upgrade write failure'); END`); err != nil {
		t.Fatal(err)
	}
	if stats, err := Index(repo, db, Options{Workers: 1}); err != nil || stats.WriteErrors != 1 || stats.FilesIndexed != 0 {
		t.Fatalf("failed upgrade: %+v, %v", stats, err)
	}
	if got, err := store.GetMeta(metaIndexFormat); err != nil || got != "2" {
		t.Fatalf("failed upgrade format = %q, %v; want 2", got, err)
	}
	if _, err := store.db.Exec(`DROP TRIGGER fail_upgrade`); err != nil {
		t.Fatal(err)
	}
	if stats, err := Index(repo, db, Options{Workers: 1}); err != nil || stats.Errors != 0 || stats.FilesIndexed != 1 {
		t.Fatalf("upgrade retry: %+v, %v; want unchanged file reparsed", stats, err)
	}
	if got, err := store.GetMeta(metaIndexFormat); err != nil || got != indexFormat {
		t.Fatalf("successful upgrade format = %q, %v; want %q", got, err, indexFormat)
	}
}

func TestIndexFormatUpgradeRetriesUnreadableFiles(t *testing.T) {
	repo, db := freshnessFixture(t)
	t.Cleanup(CloseAll)
	path := filepath.Join(repo, "main.go")
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(path, 0o600) })
	if _, err := os.ReadFile(path); err == nil {
		t.Skip("environment permits reading files without read permission")
	}
	store, err := OpenStore(db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SetMeta(metaIndexFormat, "2"); err != nil {
		t.Fatal(err)
	}
	if stats, err := Index(repo, db, Options{Workers: 1}); err != nil || stats.ParseErrors != 1 || stats.FilesIndexed != 0 {
		t.Fatalf("unreadable upgrade: %+v, %v", stats, err)
	}
	if got, err := store.GetMeta(metaIndexFormat); err != nil || got != "2" {
		t.Fatalf("failed upgrade format = %q, %v; want 2", got, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if stats, err := Index(repo, db, Options{Workers: 1}); err != nil || stats.Errors != 0 || stats.FilesIndexed != 1 {
		t.Fatalf("upgrade retry: %+v, %v; want unchanged file reparsed", stats, err)
	}
	if got, err := store.GetMeta(metaIndexFormat); err != nil || got != indexFormat {
		t.Fatalf("successful upgrade format = %q, %v; want %q", got, err, indexFormat)
	}
}
