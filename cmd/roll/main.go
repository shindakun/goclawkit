// Command roll is the worked goclaw plugin demo: a dice-roller tool. The plugin IS
// the command (mirroring godoorkit's cmd/<name>-door): the goclaw host launches this
// binary and speaks the framed stdio protocol to it. main() just wires the tool and
// calls plugin.ServeTool.
//
// Run it standalone for a sanity check without the host (the godoorkit -local
// parallel):
//
//	go build -o roll ./cmd/roll
//	./roll -selftest
//	./roll -selftest -notation 3d8
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"

	"github.com/shindakun/goclawkit/pkg/plugin"
)

const version = "1.0.0"

// rollTool rolls dice in NdM notation and returns the rolls and their sum.
type rollTool struct{}

func (rollTool) Info() plugin.ToolInfo {
	return plugin.ToolInfo{
		Name: "roll",
		Description: "Roll dice in NdM notation (e.g. 2d6) and return the total and " +
			"individual rolls. Use when the user asks to roll dice or wants a random " +
			"number in a range.",
		InputSchema: json.RawMessage(`{` +
			`"type":"object",` +
			`"properties":{"notation":{"type":"string","pattern":"^[0-9]*d[0-9]+$",` +
			`"description":"dice in NdM form, e.g. d20, 2d6, 3d8"}},` +
			`"required":["notation"]}`),
	}
}

func (rollTool) Invoke(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Notation string `json:"notation"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	return roll(in.Notation)
}

// roll parses NdM and returns text like "2d6 -> [4, 5] = 9". N defaults to 1 when
// omitted ("d20" == "1d20"). It validates ranges so junk input exercises the
// error-result path.
func roll(notation string) (string, error) {
	notation = strings.TrimSpace(notation)
	nStr, mStr, ok := strings.Cut(notation, "d")
	if !ok {
		return "", errors.New(`notation must be NdM, e.g. "2d6" or "d20"`)
	}

	n := 1
	if nStr != "" {
		var err error
		n, err = strconv.Atoi(nStr)
		if err != nil {
			return "", fmt.Errorf("bad dice count %q: %w", nStr, err)
		}
	}
	m, err := strconv.Atoi(mStr)
	if err != nil {
		return "", fmt.Errorf("bad side count %q: %w", mStr, err)
	}

	if n < 1 || n > 100 {
		return "", fmt.Errorf("dice count %d out of range (1..100)", n)
	}
	if m < 2 || m > 1000 {
		return "", fmt.Errorf("side count %d out of range (2..1000)", m)
	}

	rolls := make([]int, n)
	sum := 0
	for i := range rolls {
		rolls[i] = rand.Intn(m) + 1 // package-level rand: determinism is not required for a demo
		sum += rolls[i]
	}

	parts := make([]string, n)
	for i, r := range rolls {
		parts[i] = strconv.Itoa(r)
	}
	return fmt.Sprintf("%s -> [%s] = %d", notation, strings.Join(parts, ", "), sum), nil
}

func main() {
	selftest := flag.Bool("selftest", false, "invoke the tool once locally and print the result, then exit (no host needed)")
	notation := flag.String("notation", "2d6", "dice notation for -selftest")
	flag.Parse()

	if *selftest {
		args, _ := json.Marshal(map[string]string{"notation": *notation})
		text, err := rollTool{}.Invoke(context.Background(), args)
		if err != nil {
			fmt.Fprintf(os.Stderr, "selftest error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(text)
		return
	}

	// Serve blocks on the host handshake; if a human ran this in a terminal, the SDK
	// prints a hint to stderr so it does not look like a hang (see plugin.Serve).
	if err := plugin.ServeTool(rollTool{}, "roll", version); err != nil {
		plugin.Logf("roll", "exited: %v", err)
		os.Exit(1)
	}
}
