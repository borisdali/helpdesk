// hashapikey hashes a plaintext API key with Argon2id for use in users.yaml.
//
// Usage:
//
//	hashapikey <plaintext-key>
//	hashapikey          # reads key from stdin (no echo)
package main

import (
	"fmt"
	"os"

	"golang.org/x/term"
	"helpdesk/internal/identity"
)

func main() {
	var key string

	if len(os.Args) >= 2 {
		key = os.Args[1]
	} else {
		// Read from stdin without echo when attached to a terminal.
		if term.IsTerminal(int(os.Stdin.Fd())) {
			fmt.Fprint(os.Stderr, "API key: ")
			b, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(os.Stderr) // newline after the hidden input
			if err != nil {
				fmt.Fprintf(os.Stderr, "error reading key: %v\n", err)
				os.Exit(1)
			}
			key = string(b)
		} else {
			// Non-interactive: read from pipe.
			buf := make([]byte, 4096)
			n, err := os.Stdin.Read(buf)
			if err != nil || n == 0 {
				fmt.Fprintln(os.Stderr, "usage: hashapikey <key>  or  echo -n <key> | hashapikey")
				os.Exit(1)
			}
			key = string(buf[:n])
		}
	}

	if key == "" {
		fmt.Fprintln(os.Stderr, "error: key must not be empty")
		os.Exit(1)
	}

	hash, err := identity.HashAPIKey(key)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(hash)
}
