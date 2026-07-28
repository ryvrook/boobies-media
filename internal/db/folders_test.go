package db_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"boobies-media/internal/db"
	"boobies-media/internal/dbtest"
)

func mustFolder(t *testing.T, store *db.Store, parentID int64, name string) *db.Folder {
	t.Helper()
	folder, err := store.CreateFolder(context.Background(), parentID, name)
	if err != nil {
		t.Fatalf("CreateFolder(%d, %q): %v", parentID, name, err)
	}
	return folder
}

func TestCreateFolderAtRootAndNested(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)

	root := mustFolder(t, store, 0, "memes")
	if root.ParentID != 0 {
		t.Errorf("ParentID = %d, want 0 for a root folder", root.ParentID)
	}
	child := mustFolder(t, store, root.ID, "reaction")
	if child.ParentID != root.ID {
		t.Errorf("ParentID = %d, want %d", child.ParentID, root.ID)
	}

	got, err := store.FolderByID(ctx, child.ID)
	if err != nil {
		t.Fatalf("FolderByID: %v", err)
	}
	if got.Name != "reaction" {
		t.Errorf("Name = %q, want \"reaction\"", got.Name)
	}
}

func TestCreateFolderRejectsDuplicateNames(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	parent := mustFolder(t, store, 0, "memes")

	mustFolder(t, store, parent.ID, "reaction")
	if _, err := store.CreateFolder(ctx, parent.ID, "reaction"); err == nil {
		t.Error("a duplicate name inside one parent was accepted")
	}
	// SQLite treats NULLs as distinct, so root duplicates need the partial index.
	mustFolder(t, store, 0, "clips")
	if _, err := store.CreateFolder(ctx, 0, "clips"); err == nil {
		t.Error("a duplicate root folder name was accepted")
	}
}

func TestCreateFolderValidatesInput(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	for _, name := range []string{"", "   ", "a/b"} {
		if _, err := store.CreateFolder(ctx, 0, name); err == nil {
			t.Errorf("CreateFolder(%q) succeeded, want an error", name)
		}
	}
	if _, err := store.CreateFolder(ctx, 9999, "orphan"); err == nil {
		t.Error("CreateFolder under a nonexistent parent succeeded")
	}
}

func TestMoveFolderRejectsCycles(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)

	// a > b > c
	a := mustFolder(t, store, 0, "a")
	b := mustFolder(t, store, a.ID, "b")
	c := mustFolder(t, store, b.ID, "c")

	t.Run("into itself", func(t *testing.T) {
		if err := store.MoveFolder(ctx, a.ID, a.ID); !errors.Is(err, db.ErrFolderCycle) {
			t.Fatalf("MoveFolder(a, a) = %v, want ErrFolderCycle", err)
		}
	})
	t.Run("into a direct child", func(t *testing.T) {
		if err := store.MoveFolder(ctx, a.ID, b.ID); !errors.Is(err, db.ErrFolderCycle) {
			t.Fatalf("MoveFolder(a, b) = %v, want ErrFolderCycle", err)
		}
	})
	t.Run("into a deeper descendant", func(t *testing.T) {
		if err := store.MoveFolder(ctx, a.ID, c.ID); !errors.Is(err, db.ErrFolderCycle) {
			t.Fatalf("MoveFolder(a, c) = %v, want ErrFolderCycle", err)
		}
	})

	// The tree must be untouched after every rejection.
	got, err := store.FolderByID(ctx, a.ID)
	if err != nil {
		t.Fatalf("FolderByID: %v", err)
	}
	if got.ParentID != 0 {
		t.Errorf("a.ParentID = %d after rejected moves, want 0", got.ParentID)
	}
}

func TestMoveFolderAllowsLegalMoves(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	a := mustFolder(t, store, 0, "a")
	b := mustFolder(t, store, a.ID, "b")
	other := mustFolder(t, store, 0, "other")

	if err := store.MoveFolder(ctx, b.ID, other.ID); err != nil {
		t.Fatalf("MoveFolder(b, other): %v", err)
	}
	got, _ := store.FolderByID(ctx, b.ID)
	if got.ParentID != other.ID {
		t.Errorf("ParentID = %d, want %d", got.ParentID, other.ID)
	}

	if err := store.MoveFolder(ctx, b.ID, 0); err != nil {
		t.Fatalf("MoveFolder(b, root): %v", err)
	}
	got, _ = store.FolderByID(ctx, b.ID)
	if got.ParentID != 0 {
		t.Errorf("ParentID = %d after moving to root, want 0", got.ParentID)
	}
}

func TestFolderPathIsARootFirstBreadcrumb(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	a := mustFolder(t, store, 0, "a")
	b := mustFolder(t, store, a.ID, "b")
	c := mustFolder(t, store, b.ID, "c")

	path, err := store.FolderPath(ctx, c.ID)
	if err != nil {
		t.Fatalf("FolderPath: %v", err)
	}
	want := []string{"a", "b", "c"}
	if len(path) != len(want) {
		t.Fatalf("path has %d entries, want %d", len(path), len(want))
	}
	for i, name := range want {
		if path[i].Name != name {
			t.Errorf("path[%d] = %q, want %q", i, path[i].Name, name)
		}
	}
}

func TestDeleteFolderCascadesAndUnfilesItems(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "aiden", false)
	parent := mustFolder(t, store, 0, "parent")
	child := mustFolder(t, store, parent.ID, "child")

	item := mustCreateItem(t, store, "hash-a", user.ID)
	if err := store.MoveItem(ctx, item.ID, child.ID); err != nil {
		t.Fatalf("MoveItem: %v", err)
	}

	if err := store.DeleteFolder(ctx, parent.ID); err != nil {
		t.Fatalf("DeleteFolder: %v", err)
	}
	if _, err := store.FolderByID(ctx, child.ID); !errors.Is(err, db.ErrNotFound) {
		t.Error("the child folder survived its parent's deletion")
	}
	// Items must survive and fall back to the root, never be deleted.
	got, err := store.ItemByID(ctx, item.ID)
	if err != nil {
		t.Fatalf("the item was deleted with its folder: %v", err)
	}
	if got.FolderID != 0 {
		t.Errorf("FolderID = %d, want 0 (unfiled)", got.FolderID)
	}
}

func TestRenameFolderReportsFriendlyErrorOnSiblingCollision(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	parent := mustFolder(t, store, 0, "memes")
	mustFolder(t, store, parent.ID, "reaction")
	moving := mustFolder(t, store, parent.ID, "clips")

	err := store.RenameFolder(ctx, moving.ID, "reaction")
	if err == nil {
		t.Fatal("RenameFolder onto an existing sibling name succeeded, want an error")
	}
	if strings.Contains(err.Error(), "UNIQUE constraint failed") {
		t.Errorf("RenameFolder leaked the raw driver error: %v", err)
	}
}

func TestMoveFolderReportsFriendlyErrorOnDestinationCollision(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	parentA := mustFolder(t, store, 0, "a")
	parentB := mustFolder(t, store, 0, "b")
	mustFolder(t, store, parentB.ID, "clips")
	moving := mustFolder(t, store, parentA.ID, "clips")

	err := store.MoveFolder(ctx, moving.ID, parentB.ID)
	if err == nil {
		t.Fatal("MoveFolder into a parent already holding that name succeeded, want an error")
	}
	if strings.Contains(err.Error(), "UNIQUE constraint failed") {
		t.Errorf("MoveFolder leaked the raw driver error: %v", err)
	}
}

func TestMoveFolderToRootReportsFriendlyErrorOnCollision(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	mustFolder(t, store, 0, "clips")
	parent := mustFolder(t, store, 0, "parent")
	moving := mustFolder(t, store, parent.ID, "clips")

	// This trips the partial index (folders_root_name), not the composite
	// UNIQUE(parent_id, name) constraint, since the destination is the root.
	err := store.MoveFolder(ctx, moving.ID, 0)
	if err == nil {
		t.Fatal("MoveFolder to root over an existing root folder name succeeded, want an error")
	}
	if strings.Contains(err.Error(), "UNIQUE constraint failed") {
		t.Errorf("MoveFolder leaked the raw driver error: %v", err)
	}
}

func TestListFolders(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	a := mustFolder(t, store, 0, "zeta")
	mustFolder(t, store, 0, "alpha")
	mustFolder(t, store, a.ID, "nested")

	folders, err := store.ListFolders(ctx)
	if err != nil {
		t.Fatalf("ListFolders: %v", err)
	}
	if len(folders) != 3 {
		t.Fatalf("got %d folders, want 3", len(folders))
	}
	// Roots first, alphabetically, so a tree can be built in one pass.
	if folders[0].Name != "alpha" || folders[1].Name != "zeta" {
		t.Errorf("order = %q, %q; want roots alphabetically first", folders[0].Name, folders[1].Name)
	}
}

func TestListChildFoldersReturnsOnlyImmediateChildren(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	parent := mustFolder(t, store, 0, "parent")
	child := mustFolder(t, store, parent.ID, "child")
	mustFolder(t, store, child.ID, "grandchild")
	mustFolder(t, store, 0, "other")

	children, err := store.ListChildFolders(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListChildFolders: %v", err)
	}
	if len(children) != 1 || children[0].ID != child.ID {
		t.Fatalf("children = %+v, want only %q", children, child.Name)
	}
}

func TestFolderPreviewItemsIncludesNestedMedia(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "preview-owner", false)
	parent := mustFolder(t, store, 0, "parent")
	child := mustFolder(t, store, parent.ID, "child")
	direct := mustCreateItem(t, store, "direct-preview", user.ID)
	nested := mustCreateItem(t, store, "nested-preview", user.ID)
	outside := mustCreateItem(t, store, "outside-preview", user.ID)
	if err := store.MoveItem(ctx, direct.ID, parent.ID); err != nil {
		t.Fatalf("move direct item: %v", err)
	}
	if err := store.MoveItem(ctx, nested.ID, child.ID); err != nil {
		t.Fatalf("move nested item: %v", err)
	}

	items, err := store.FolderPreviewItems(ctx, parent.ID, 4)
	if err != nil {
		t.Fatalf("FolderPreviewItems: %v", err)
	}
	got := map[string]bool{}
	for _, item := range items {
		got[item.ID] = true
	}
	if !got[direct.ID] || !got[nested.ID] {
		t.Errorf("preview IDs = %v, want direct and nested media", got)
	}
	if got[outside.ID] {
		t.Error("folder preview included media outside its subtree")
	}
}

func TestMoveFolderItemsBatchMovesInSections(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "batch-move-owner", false)
	source := mustFolder(t, store, 0, "source")
	destination := mustFolder(t, store, 0, "destination")
	for _, hash := range []string{"move-a", "move-b", "move-c"} {
		item := mustCreateItem(t, store, hash, user.ID)
		if err := store.MoveItem(ctx, item.ID, source.ID); err != nil {
			t.Fatalf("MoveItem: %v", err)
		}
	}

	moved, more, err := store.MoveFolderItemsBatch(ctx, source.ID, destination.ID, 2)
	if err != nil || moved != 2 || !more {
		t.Fatalf("first batch = moved %d, more %v, err %v; want 2, true, nil", moved, more, err)
	}
	moved, more, err = store.MoveFolderItemsBatch(ctx, source.ID, destination.ID, 2)
	if err != nil || moved != 1 || more {
		t.Fatalf("second batch = moved %d, more %v, err %v; want 1, false, nil", moved, more, err)
	}
}
