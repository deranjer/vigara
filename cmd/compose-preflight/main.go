package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/deranjer/vigara/internal/composepreflight"
)

type input struct {
	Requirements composepreflight.RequirementModel `json:"requirements"`
	Host         composepreflight.HostSnapshot     `json:"host"`
}

func main() {
	path := flag.String("input", "-", "JSON preflight input file, or - for stdin")
	flag.Parse()
	var src io.Reader = os.Stdin
	if *path != "-" {
		f, err := os.Open(*path)
		if err != nil {
			fail()
		}
		defer f.Close()
		src = f
	} else {
		src = os.Stdin
	}
	report, err := evaluateInput(src, time.Now().UTC())
	if err != nil {
		fail()
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(true)
	if enc.Encode(report) != nil {
		fail()
	}
}
func fail() {
	fmt.Fprintln(os.Stderr, "compose-preflight: invalid input or output error; details omitted")
	os.Exit(2)
}

func evaluateInput(src io.Reader, now time.Time) (composepreflight.Report, error) {
	raw, err := io.ReadAll(io.LimitReader(src, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return composepreflight.Report{}, fmt.Errorf("invalid input")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var in input
	if dec.Decode(&in) != nil {
		return composepreflight.Report{}, fmt.Errorf("invalid input")
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return composepreflight.Report{}, fmt.Errorf("invalid input")
	}
	return composepreflight.Evaluate(in.Requirements, in.Host, now), nil
}
