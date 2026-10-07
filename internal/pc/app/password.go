package app

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/onnov/mcp/internal/pc/ownerauth"
	"golang.org/x/term"
)

func hashPassword() error {
	var password []byte
	var err error
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprint(os.Stderr, "Owner password (16..72 bytes): ")
		password, err = term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
	} else {
		password, err = io.ReadAll(io.LimitReader(os.Stdin, 74))
		password = []byte(strings.TrimSuffix(strings.TrimSuffix(string(password), "\n"), "\r"))
	}
	if err != nil {
		return err
	}
	defer func() {
		for i := range password {
			password[i] = 0
		}
	}()
	hash, err := ownerauth.HashPassword(password)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, string(hash))
	return nil
}
