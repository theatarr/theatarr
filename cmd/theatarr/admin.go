package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/store"
)

func init() {
	register(command{
		name:    "admin",
		summary: "account tools: reset-password, create-key",
		run:     runAdmin,
	})
}

func runAdmin(args []string) error {
	switch {
	case len(args) > 0 && args[0] == "reset-password":
		return resetPassword(args[1:], os.Stdin, os.Stdout)
	case len(args) > 0 && args[0] == "create-key":
		return createKey(args[1:], os.Stdout)
	}
	fmt.Fprint(os.Stderr, "usage: theatarr admin reset-password [--db path] [--username admin] [--password-stdin]\n"+
		"       theatarr admin create-key [--db path] -user NAME -name KEYNAME\n")
	if helpArg(args) {
		return flag.ErrHelp
	}
	return errors.New("want: reset-password or create-key")
}

// createKey makes an API key for an account and prints it once on stdout, for an agent that has only a shell on the
// host (already full trust). The key carries the account's permissions; an admin's key can do what the admin can.
func createKey(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("admin create-key", flag.ContinueOnError)
	dbPath := fs.String("db", defaultDBPath(), "database file (env THEATARR_DB)")
	username := fs.String("user", "", "account the key belongs to; the key carries its permissions")
	name := fs.String("name", "", "key name, e.g. agent")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	if *username == "" || *name == "" {
		return errors.New("want -user and -name")
	}
	ctx := context.Background()
	st, err := store.Open(ctx, *dbPath, store.Options{})
	if err != nil {
		return err
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		return err
	}
	key, _, err := auth.CreateUserKey(ctx, st, *username, *name)
	if errors.Is(err, auth.ErrUserNotFound) {
		return fmt.Errorf("no account %q", *username)
	}
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, key)
	return nil
}

// resetPassword sets a new password for a user (creating it if needed) and ends its sessions. Without
// --password-stdin it generates one and prints it once. It migrates the database first, so it also works before the
// service has ever started.
func resetPassword(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("admin reset-password", flag.ContinueOnError)
	dbPath := fs.String("db", defaultDBPath(), "database file (env THEATARR_DB)")
	username := fs.String("username", auth.AdminUsername, "account to reset")
	fromStdin := fs.Bool("password-stdin", false, "read the new password from the first line of stdin instead of generating one")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	var password string
	if *fromStdin {
		line, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		password = strings.TrimRight(line, "\r\n")
	} else {
		var err error
		if password, err = auth.RandomPassword(); err != nil {
			return err
		}
	}
	ctx := context.Background()
	st, err := store.Open(ctx, *dbPath, store.Options{})
	if err != nil {
		return err
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		return err
	}
	if err := auth.ResetPassword(ctx, st, *username, password); err != nil {
		return err
	}
	if *username == auth.AdminUsername {
		// The first start's password is stale now.
		dir := filepath.Dir(*dbPath)
		if removed, err := auth.RemoveInitialPassword(dir); err != nil {
			return fmt.Errorf("password set, but removing %s: %w", filepath.Join(dir, auth.InitialPasswordFile), err)
		} else if removed {
			fmt.Fprintf(stdout, "removed %s\n", filepath.Join(dir, auth.InitialPasswordFile))
		}
	}
	if *fromStdin {
		fmt.Fprintf(stdout, "password for %s set; its sessions are ended\n", *username)
	} else {
		fmt.Fprintf(stdout, "new password for %s (shown once): %s\n", *username, password)
	}
	return nil
}
