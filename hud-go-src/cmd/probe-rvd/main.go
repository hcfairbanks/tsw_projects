package main

import (
	"encoding/json"
	"fmt"
	"os"

	"hud-go/internal/pak/uasset"
)

func main() {
	rvd, err := uasset.ParseCookedRVD(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERR:", err)
		os.Exit(1)
	}
	b, _ := json.MarshalIndent(rvd, "", "  ")
	fmt.Println(string(b))
}
