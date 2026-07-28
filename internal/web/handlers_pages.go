package web

import (
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"boobies-media/internal/db"
)

// folderNode is one row of the flattened folder tree the Folders rail walks
// to render indentation. Depth 0 is a root folder (parent_id NULL).
type folderNode struct {
	Folder *db.Folder
	Depth  int
}

// IndentedName is the folder name prefixed with two non-breaking spaces per
// level of depth. The lightbox's folder <select> needs it because option text
// cannot be indented with CSS, and ordinary leading spaces collapse.
func (n folderNode) IndentedName() string {
	return strings.Repeat("\u00a0\u00a0", n.Depth) + n.Folder.Name
}

// browseData is what the browse template renders.
type browseData struct {
	Items      []map[string]any
	NextCursor string
	Sort       string
	Query      string

	// Folders is the whole tree, root-first, depth-annotated for indentation.
	// The rail renders it as plain filter links, so folder navigation works
	// with JavaScript off; creating, renaming, moving and deleting is the
	// folders island's job over /api/folders, and it reloads the page rather
	// than patching this server-derived markup in place. The lightbox's
	// folder-move select is rendered from the same slice.
	Folders         []folderNode
	HasActiveFolder bool
	ActiveFolderID  int64
	FolderPath      []*db.Folder
	// FolderPathLastIndex is len(FolderPath)-1, computed here rather than
	// with the template's len builtin: len panics on the zero Value a nil
	// .Data produces (render_test.go's TestBrowsePageShowsDisplayName
	// renders this page with Data unset), where plain field access and range
	// both degrade gracefully instead.
	FolderPathLastIndex int

	Tags      []string
	ActiveTag string

	Uploaders      []*db.User
	ActiveUploader int64

	// ItemCount is len(Items), for the same reason as FolderPathLastIndex.
	ItemCount int

	// UploaderDirectory maps a numeric user id to its username, as a JSON
	// object literal. The /api/items item shape only carries the numeric
	// `uploader` id (that is the committed API contract), so the lightbox
	// island reads this alongside the page instead of the contract growing a
	// field just for display. Built from validated usernames only (see
	// db.NormalizeUsername), so embedding it unescaped as a script body is
	// safe.
	UploaderDirectory template.JS
}

// handleBrowse serves GET /. The first page of thumbnails is rendered
// server-side so the grid is populated before any JavaScript runs; the grid
// island continues from NextCursor. The Folders/Tags/Uploaders rails and the
// active filter/sort state are also rendered server-side, from the same
// query the API itself parses, so a bookmarked filtered URL renders correctly
// with no JavaScript at all.
func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	user, ok := CurrentUser(r)
	if !ok {
		// Defence in depth: the gate should have already handled this.
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}

	query, err := s.listItemQuery(r)
	if err != nil {
		// A bad sort (or filter) in a bookmarked URL should not be a hard error.
		query = db.ItemQuery{}
	}
	items, next, err := s.Store.ListItems(r.Context(), query)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	payload, err := s.itemsPayload(r, items)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	sort := r.URL.Query().Get("sort")
	if sort == "" {
		sort = "newest"
	}

	folders, err := s.Store.ListFolders(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	tags, err := s.Store.ListTags(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	uploaders, err := s.Store.ListUsers(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	var folderPath []*db.Folder
	if query.FolderID != nil && *query.FolderID != 0 {
		folderPath, err = s.Store.FolderPath(r.Context(), *query.FolderID)
		if err != nil && !errors.Is(err, db.ErrNotFound) {
			s.serverError(w, r, err)
			return
		}
	}

	directory := make(map[string]string, len(uploaders))
	for _, u := range uploaders {
		directory[strconv.FormatInt(u.ID, 10)] = u.Username
	}
	directoryJSON, err := json.Marshal(directory)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	var activeFolderID int64
	if query.FolderID != nil {
		activeFolderID = *query.FolderID
	}

	data := PageData{
		Title:   "Browse",
		SiteURL: strings.TrimRight(s.Cfg.BaseURL, "/"),
		Storage: s.storageUsage(r.Context()),
		User:    user,
		Data: browseData{
			Items:      payload,
			NextCursor: next,
			Sort:       sort,
			Query:      r.URL.Query().Get("q"),

			Folders:             buildFolderTree(folders),
			HasActiveFolder:     query.FolderID != nil,
			ActiveFolderID:      activeFolderID,
			FolderPath:          folderPath,
			FolderPathLastIndex: len(folderPath) - 1,

			Tags:      tags,
			ActiveTag: query.Tag,

			Uploaders:      uploaders,
			ActiveUploader: query.UploaderID,

			ItemCount: len(payload),

			UploaderDirectory: template.JS(directoryJSON), //nolint:gosec // built from validated usernames above
		},
	}
	if err := s.Renderer.Render(w, http.StatusOK, "browse", data); err != nil {
		s.serverError(w, r, err)
	}
}

// buildFolderTree flattens the store's folder list into root-first,
// depth-annotated rows in a single pass, independent of the slice order
// ListFolders happens to return (which groups by parent id, not by subtree).
func buildFolderTree(folders []*db.Folder) []folderNode {
	children := map[int64][]*db.Folder{}
	for _, f := range folders {
		children[f.ParentID] = append(children[f.ParentID], f)
	}
	var out []folderNode
	var walk func(parentID int64, depth int)
	walk = func(parentID int64, depth int) {
		for _, f := range children[parentID] {
			out = append(out, folderNode{Folder: f, Depth: depth})
			walk(f.ID, depth+1)
		}
	}
	walk(0, 0)
	return out
}
