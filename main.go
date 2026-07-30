package main

import (
	"fmt"
	"os"

	"github.com/JonathanAriass/ccs/internal/session"
)

func main() {
	sessions, err := session.Load(session.Dir())
	if err != nil {
		fmt.Fprintln(os.Stderr, "ccs:", err)
		os.Exit(1)
	}
	fmt.Printf("ccs: found %d session(s) in %s\n", len(sessions), session.Dir())
}
