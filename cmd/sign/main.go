// Command sign makes the release signing key and signs release checksums. See `just publish`.
package main

import (
	"fmt"
	"os"

	"github.com/BikaSolution/bikasend/pkg/sign"
)

func main() {
	if err := sign.Main(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
