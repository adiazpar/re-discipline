package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/adiazpar/re-discipline/server/maintenance"
)

func main() {
	planPath := flag.String("plan", "", "reviewed cleanup plan")
	output := flag.String("out", "", "output SQL file")
	batch := flag.Int("batch", 100, "maximum findings per transaction")
	flag.Parse()
	if *planPath == "" || *output == "" {
		panic("provide -plan and -out")
	}
	b, err := os.ReadFile(*planPath)
	if err != nil {
		panic(err)
	}
	var plan maintenance.CleanupPlan
	if err = json.Unmarshal(b, &plan); err != nil {
		panic(err)
	}
	parts, err := maintenance.CleanupSQL(plan, *batch)
	if err != nil {
		panic(err)
	}
	if err = os.WriteFile(*output, []byte(strings.Join(parts, "\n")), 0600); err != nil {
		panic(err)
	}
	fmt.Printf("Prepared %d revision-checked transactions for %d findings.\n", len(parts), len(plan.Items))
}
