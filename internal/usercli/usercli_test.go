package usercli

import (
	"bufio"
	"bytes"
	"context"
	"strings"
	"testing"

	"boobies-media/internal/auth"
	"boobies-media/internal/dbtest"
)

func fixedPassword(pw string) func(string) (string, error) {
	return func(string) (string, error) { return pw, nil }
}

// TestPromptPasswordPipedTwice exercises the real non-TTY password-reading
// path (not the fixedPassword test stub) to guard against the regression
// where a fresh bufio.Reader per call buffers both piped lines on the first
// read and starves the second, turning every non-interactive `user add`
// into a spurious "passwords do not match".
func TestPromptPasswordPipedTwice(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("hunter2\nhunter2\n"))

	pw1, err := promptPasswordFrom(r, "Password: ")
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	if pw1 != "hunter2" {
		t.Fatalf("first read = %q, want %q", pw1, "hunter2")
	}

	pw2, err := promptPasswordFrom(r, "Confirm password: ")
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if pw2 != "hunter2" {
		t.Fatalf("second read = %q, want %q", pw2, "hunter2")
	}
}

func TestUserAddCreatesAnAccount(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	var out bytes.Buffer

	err := Run(ctx, store, []string{"user", "add", "aiden", "--display-name", "Aiden S", "--admin"},
		strings.NewReader(""), &out, fixedPassword("hunter2"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	user, err := store.UserByUsername(ctx, "aiden")
	if err != nil {
		t.Fatalf("UserByUsername: %v", err)
	}
	if user.DisplayName != "Aiden S" {
		t.Errorf("DisplayName = %q, want \"Aiden S\"", user.DisplayName)
	}
	if !user.IsAdmin {
		t.Error("IsAdmin = false, want true")
	}

	ok, err := auth.VerifyPassword(user.PasswordHash, "hunter2")
	if err != nil || !ok {
		t.Errorf("the stored password does not verify: ok=%v err=%v", ok, err)
	}
	if user.PasswordHash == "hunter2" {
		t.Fatal("the password was stored in plaintext")
	}
	if user.APIKeyHash == "" {
		t.Error("no API key hash was stored")
	}
}

// TestUserAddSucceedsWithPipedStdin drives Run end-to-end with readPassword
// left nil (the production wiring) and two piped lines of stdin, the same
// shape as `printf 'hunter2\nhunter2\n' | server user add <name>`. It uses
// the real promptPasswordFrom path via Run's internal shared reader, not the
// fixedPassword stub, so it would have caught the per-call-fresh-reader bug.
func TestUserAddSucceedsWithPipedStdin(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	var out bytes.Buffer

	err := Run(ctx, store, []string{"user", "add", "piped"},
		strings.NewReader("hunter2\nhunter2\n"), &out, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	user, err := store.UserByUsername(ctx, "piped")
	if err != nil {
		t.Fatalf("UserByUsername: %v", err)
	}
	ok, err := auth.VerifyPassword(user.PasswordHash, "hunter2")
	if err != nil || !ok {
		t.Errorf("the stored password does not verify: ok=%v err=%v", ok, err)
	}
}

func TestUserAddPrintsTheAPIKeyExactlyOnce(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	var out bytes.Buffer

	if err := Run(ctx, store, []string{"user", "add", "bot"}, strings.NewReader(""), &out, fixedPassword("pw")); err != nil {
		t.Fatalf("Run: %v", err)
	}
	printed := out.String()
	if !strings.Contains(printed, "bm_") {
		t.Fatalf("output does not contain the plaintext API key: %q", printed)
	}

	// Extract the key and confirm only its hash reached the database.
	var key string
	for _, field := range strings.Fields(printed) {
		if strings.HasPrefix(field, "bm_") {
			key = field
			break
		}
	}
	if key == "" {
		t.Fatal("could not find the printed API key")
	}
	user, err := store.UserByUsername(ctx, "bot")
	if err != nil {
		t.Fatalf("UserByUsername: %v", err)
	}
	if user.APIKeyHash != auth.HashToken(key) {
		t.Error("the stored api_key_hash is not the SHA-256 of the printed key")
	}
	if user.APIKeyHash == key {
		t.Fatal("the plaintext API key was stored in the database")
	}
}

func TestUserAddDefaultsDisplayNameToUsername(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	if err := Run(ctx, store, []string{"user", "add", "mia"}, strings.NewReader(""), &bytes.Buffer{}, fixedPassword("pw")); err != nil {
		t.Fatalf("Run: %v", err)
	}
	user, err := store.UserByUsername(ctx, "mia")
	if err != nil {
		t.Fatalf("UserByUsername: %v", err)
	}
	if user.DisplayName != "mia" {
		t.Errorf("DisplayName = %q, want the username as the fallback", user.DisplayName)
	}
}

func TestUserAddRejectsDuplicates(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	args := []string{"user", "add", "aiden"}
	if err := Run(ctx, store, args, strings.NewReader(""), &bytes.Buffer{}, fixedPassword("pw")); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	err := Run(ctx, store, args, strings.NewReader(""), &bytes.Buffer{}, fixedPassword("pw"))
	if err == nil {
		t.Fatal("creating a duplicate user succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error = %v, want it to mention that the username already exists", err)
	}
}

func TestUserAddRejectsEmptyPassword(t *testing.T) {
	err := Run(context.Background(), dbtest.New(t), []string{"user", "add", "aiden"},
		strings.NewReader(""), &bytes.Buffer{}, fixedPassword(""))
	if err == nil {
		t.Fatal("an empty password was accepted, want an error")
	}
}

func TestUserAddRejectsBadUsername(t *testing.T) {
	err := Run(context.Background(), dbtest.New(t), []string{"user", "add", "not a valid name"},
		strings.NewReader(""), &bytes.Buffer{}, fixedPassword("pw"))
	if err == nil {
		t.Fatal("an invalid username was accepted, want an error")
	}
}

func TestUserAddRequiresAUsername(t *testing.T) {
	err := Run(context.Background(), dbtest.New(t), []string{"user", "add"},
		strings.NewReader(""), &bytes.Buffer{}, fixedPassword("pw"))
	if err == nil {
		t.Fatal("`user add` with no username succeeded, want a usage error")
	}
}

func TestUserList(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	for _, name := range []string{"zoe", "aiden"} {
		if err := Run(ctx, store, []string{"user", "add", name}, strings.NewReader(""), &bytes.Buffer{}, fixedPassword("pw")); err != nil {
			t.Fatalf("add %s: %v", name, err)
		}
	}

	var out bytes.Buffer
	if err := Run(ctx, store, []string{"user", "list"}, strings.NewReader(""), &out, nil); err != nil {
		t.Fatalf("user list: %v", err)
	}
	body := out.String()
	for _, want := range []string{"aiden", "zoe", "USERNAME"} {
		if !strings.Contains(body, want) {
			t.Errorf("`user list` output does not contain %q; got:\n%s", want, body)
		}
	}
	if strings.Index(body, "aiden") > strings.Index(body, "zoe") {
		t.Error("`user list` is not sorted by username")
	}
	// Listing must never print secrets.
	if strings.Contains(body, "$argon2id$") || strings.Contains(body, "bm_") {
		t.Error("`user list` leaked a credential")
	}
}

func TestUnknownSubcommand(t *testing.T) {
	err := Run(context.Background(), dbtest.New(t), []string{"user", "frobnicate"},
		strings.NewReader(""), &bytes.Buffer{}, nil)
	if err == nil {
		t.Fatal("an unknown subcommand succeeded, want a usage error")
	}
}
