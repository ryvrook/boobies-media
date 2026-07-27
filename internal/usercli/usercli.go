// Package usercli implements the `user` subcommands. There is no self-signup,
// so this is the only way an account is created.
package usercli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"text/tabwriter"

	"golang.org/x/term"

	"boobies-media/internal/auth"
	"boobies-media/internal/db"
)

// Usage is printed when the arguments do not parse.
const Usage = `usage:
  server user add <username> [--display-name "Name"] [--admin]
  server user list`

// Run dispatches a `user` subcommand. readPassword is injected so tests need
// no terminal; pass nil in production to read from stdin. When nil, Run
// builds a single buffered reader over stdin and shares it across every
// password prompt for the invocation, so piped input (e.g.
// `printf 'pw\npw\n' | server user add ...`) isn't buffered and discarded by
// the first read, which would otherwise starve the confirmation prompt.
func Run(ctx context.Context, store *db.Store, args []string, stdin io.Reader, stdout io.Writer, readPassword func(prompt string) (string, error)) error {
	if len(args) < 2 || args[0] != "user" {
		return errors.New(Usage)
	}
	if readPassword == nil {
		r := bufio.NewReader(stdin)
		readPassword = func(prompt string) (string, error) {
			return promptPasswordFrom(r, prompt)
		}
	}
	switch args[1] {
	case "add":
		return runAdd(ctx, store, args[2:], stdout, readPassword)
	case "list":
		return runList(ctx, store, stdout)
	default:
		return fmt.Errorf("unknown subcommand %q\n%s", args[1], Usage)
	}
}

func runAdd(ctx context.Context, store *db.Store, args []string, stdout io.Writer, readPassword func(string) (string, error)) error {
	if len(args) == 0 {
		return errors.New(Usage)
	}

	// Extract username as first positional argument, then parse remaining flags
	usernameArg := args[0]
	fs := flag.NewFlagSet("user add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	displayName := fs.String("display-name", "", "display name shown in the UI (defaults to the username)")
	isAdmin := fs.Bool("admin", false, "grant admin rights")
	if err := fs.Parse(args[1:]); err != nil {
		return fmt.Errorf("%v\n%s", err, Usage)
	}
	if fs.NArg() != 0 {
		return errors.New(Usage)
	}
	username, err := db.NormalizeUsername(usernameArg)
	if err != nil {
		return err
	}
	if readPassword == nil {
		return errors.New("usercli: no password reader configured")
	}

	password, err := readPassword("Password: ")
	if err != nil {
		return fmt.Errorf("read password: %w", err)
	}
	if password == "" {
		return errors.New("password must not be empty")
	}
	confirm, err := readPassword("Confirm password: ")
	if err != nil {
		return fmt.Errorf("read password confirmation: %w", err)
	}
	if password != confirm {
		return errors.New("passwords do not match")
	}

	passwordHash, err := auth.HashPassword(password)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	apiKey, err := auth.NewAPIKey()
	if err != nil {
		return fmt.Errorf("generate api key: %w", err)
	}

	name := *displayName
	if strings.TrimSpace(name) == "" {
		name = username
	}
	user, err := store.CreateUser(ctx, username, name, passwordHash, auth.HashToken(apiKey), *isAdmin)
	if err != nil {
		if errors.Is(err, db.ErrDuplicateUser) {
			return fmt.Errorf("a user named %q already exists", username)
		}
		return err
	}

	role := "member"
	if user.IsAdmin {
		role = "admin"
	}
	fmt.Fprintf(stdout, "Created %s (%s) as %s.\n", user.Username, user.DisplayName, role)
	fmt.Fprintf(stdout, "API key (shown once, store it now): %s\n", apiKey)
	return nil
}

func runList(ctx context.Context, store *db.Store, stdout io.Writer) error {
	users, err := store.ListUsers(ctx)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "USERNAME\tDISPLAY NAME\tADMIN\tAPI KEY\tCREATED")
	for _, u := range users {
		admin := "no"
		if u.IsAdmin {
			admin = "yes"
		}
		apiKey := "none"
		if u.APIKeyHash != "" {
			// Never print the key or its full hash.
			apiKey = "set"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			u.Username, u.DisplayName, admin, apiKey, u.CreatedAt.Format("2006-01-02"))
	}
	return tw.Flush()
}

var (
	stdinReaderOnce sync.Once
	stdinReader     *bufio.Reader
)

// PromptPassword reads a password from the process's real os.Stdin without
// echoing it. Run does not call this: it builds its own reader over the
// stdin it was given, so tests never touch the process's real stdin.
// PromptPassword exists as a standalone convenience for callers that want to
// prompt outside of Run. When stdin is not a terminal (a pipe, or CI) it
// falls back to reading one line at a time from a package-level buffered
// reader shared across calls, so sequential prompts (e.g. password then
// confirmation) each see the next piped line instead of the first call
// buffering and consuming everything.
func PromptPassword(prompt string) (string, error) {
	stdinReaderOnce.Do(func() { stdinReader = bufio.NewReader(os.Stdin) })
	return promptPasswordFrom(stdinReader, prompt)
}

// promptPasswordFrom reads a password without echoing it, when os.Stdin is a
// terminal; otherwise it reads one line from r. Callers that issue several
// prompts in sequence (password, then confirmation) must pass the same *r*
// to each call so buffered-but-unread piped input carries over instead of
// being discarded when a fresh reader is constructed per call.
func promptPasswordFrom(r *bufio.Reader, prompt string) (string, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		fmt.Fprint(os.Stderr, prompt)
		raw, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		return string(raw), nil
	}
	line, err := r.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
