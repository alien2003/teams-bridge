// Command gen-vectors writes proto/vectors.json (mise run gen-vectors).
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/alien2003/teams-bridge/internal/proto"
)

func main() {
	out := flag.String("o", "proto/vectors.json", "output file")
	flag.Parse()
	if err := os.WriteFile(*out, proto.MarshalVectors(proto.GenerateVectors()), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
