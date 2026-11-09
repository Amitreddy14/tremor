// Package cli implements the tremorctl commands. The store is opened through
// an injected function so everything here is testable without MongoDB.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Amitreddy14/tremor/internal/report"
	"github.com/Amitreddy14/tremor/internal/runstore"
)

// Opener connects to the history store and returns a function that closes it.
type Opener func(ctx context.Context, uri string) (runstore.Store, func(), error)

// Deps are the CLI's external dependencies.
type Deps struct {
	Open   Opener
	Now    func() time.Time
	Getenv func(string) string
	Stdout io.Writer
	Stderr io.Writer
}

// EnvMongoURI is the environment variable that supplies --mongo-uri.
const EnvMongoURI = "TREMOR_MONGO_URI"

const usage = `tremorctl reads Tremor's chaos experiment history from MongoDB.

Usage:
  tremorctl history [flags]      list recent runs
  tremorctl show <run-id> [flags] show one run in detail
  tremorctl report [flags]       recovery-time summary by fault type

Common flags:
  --mongo-uri string   MongoDB URI (default: $TREMOR_MONGO_URI)

Run "tremorctl <command> -h" for the flags of a command.
`

// Run executes tremorctl and returns the process exit code.
func Run(args []string, d Deps) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(d.Stdout, usage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	var err error
	switch args[0] {
	case "history":
		err = runHistory(args[1:], d)
	case "show":
		err = runShow(args[1:], d)
	case "report":
		err = runReport(args[1:], d)
	default:
		fmt.Fprintf(d.Stderr, "tremorctl: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
	switch {
	case err == nil:
		return 0
	case errors.Is(err, flag.ErrHelp):
		return 0
	default:
		fmt.Fprintf(d.Stderr, "tremorctl: %v\n", err)
		return 1
	}
}

func newFlags(name string, d Deps) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(d.Stderr)
	uri := fs.String("mongo-uri", d.Getenv(EnvMongoURI), "MongoDB URI (default: $"+EnvMongoURI+")")
	return fs, uri
}

type filterFlags struct {
	namespace, name, fault, result, since *string
}

func addFilterFlags(fs *flag.FlagSet, defaultSince string) filterFlags {
	return filterFlags{
		namespace: fs.String("namespace", "", "only runs in this namespace"),
		name:      fs.String("name", "", "only runs of this experiment name"),
		fault:     fs.String("fault", "", "only this fault type, e.g. PodKill"),
		result:    fs.String("result", "", "only this result: completed, failed, aborted, dry_run"),
		since:     fs.String("since", defaultSince, "time window such as 24h, 7d or all"),
	}
}

func (f filterFlags) build(now time.Time) (runstore.Filter, error) {
	since, err := report.ParseWindow(*f.since, now)
	if err != nil {
		return runstore.Filter{}, err
	}
	return runstore.Filter{
		Namespace: *f.namespace, Name: *f.name, FaultType: *f.fault,
		Result: *f.result, Since: since,
	}, nil
}

// connect validates the URI and opens the store with a bounded timeout.
func connect(ctx context.Context, d Deps, uri string) (runstore.Store, func(), error) {
	if strings.TrimSpace(uri) == "" {
		return nil, nil, fmt.Errorf("no MongoDB URI: pass --mongo-uri or set $%s", EnvMongoURI)
	}
	store, closeFn, err := d.Open(ctx, uri)
	if err != nil {
		return nil, nil, fmt.Errorf("connect to history store: %w", err)
	}
	if closeFn == nil {
		closeFn = func() {}
	}
	return store, closeFn, nil
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func runHistory(args []string, d Deps) error {
	fs, uri := newFlags("history", d)
	ff := addFilterFlags(fs, "all")
	limit := fs.Int("limit", 20, "maximum number of runs (0 = no limit)")
	output := fs.String("output", "table", "output format: table or json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *output != "table" && *output != "json" {
		return fmt.Errorf("invalid --output %q: use table or json", *output)
	}
	if *limit < 0 {
		return errors.New("--limit must not be negative")
	}
	filter, err := ff.build(d.Now())
	if err != nil {
		return err
	}
	filter.Limit = *limit

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	store, closeFn, err := connect(ctx, d, *uri)
	if err != nil {
		return err
	}
	defer closeFn()

	runs, err := store.List(ctx, filter)
	if err != nil {
		return err
	}
	if *output == "json" {
		if runs == nil {
			runs = []runstore.Run{}
		}
		return writeJSON(d.Stdout, runs)
	}
	if len(runs) == 0 {
		fmt.Fprintln(d.Stdout, "no runs found")
		return nil
	}
	w := tabwriter.NewWriter(d.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSTARTED\tNAMESPACE/NAME\tFAULT\tRESULT\tPOD\tUSER\tIMPACTED")
	for _, r := range runs {
		fmt.Fprintf(w, "%s\t%s\t%s/%s\t%s\t%s\t%s\t%s\t%s\n",
			shortID(r.ID), r.StartedAt.UTC().Format("2006-01-02 15:04:05"),
			r.Namespace, r.Name, r.FaultType, r.Result,
			millis(r.PodRecoveryMillis), millis(r.UserRecoveryMillis), impacted(r))
	}
	return w.Flush()
}

func runShow(args []string, d Deps) error {
	fs, uri := newFlags("show", d)
	output := fs.String("output", "text", "output format: text or json")
	// Allow the run ID anywhere among the flags: parse, take the first
	// positional argument, then parse whatever follows it.
	var id string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			break
		}
		if id == "" {
			id = fs.Arg(0)
		} else {
			return fmt.Errorf("unexpected argument %q", fs.Arg(0))
		}
		rest = fs.Args()[1:]
	}
	if id == "" {
		return errors.New("show needs a run ID: tremorctl show <run-id>")
	}
	if *output != "text" && *output != "json" {
		return fmt.Errorf("invalid --output %q: use text or json", *output)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	store, closeFn, err := connect(ctx, d, *uri)
	if err != nil {
		return err
	}
	defer closeFn()

	run, err := resolve(ctx, store, id)
	if err != nil {
		return err
	}
	if *output == "json" {
		return writeJSON(d.Stdout, run)
	}
	printRun(d.Stdout, run)
	return nil
}

// resolve finds a run by full ID, or by a unique ID prefix.
func resolve(ctx context.Context, store runstore.Store, id string) (runstore.Run, error) {
	run, err := store.Get(ctx, id)
	if err == nil {
		return run, nil
	}
	if !errors.Is(err, runstore.ErrNotFound) {
		return runstore.Run{}, err
	}
	all, lerr := store.List(ctx, runstore.Filter{})
	if lerr != nil {
		return runstore.Run{}, lerr
	}
	var matches []runstore.Run
	for _, r := range all {
		if strings.HasPrefix(r.ID, id) {
			matches = append(matches, r)
		}
	}
	switch len(matches) {
	case 0:
		return runstore.Run{}, fmt.Errorf("run %q not found", id)
	case 1:
		return matches[0], nil
	default:
		return runstore.Run{}, fmt.Errorf("run ID prefix %q is ambiguous (%d matches)", id, len(matches))
	}
}

func printRun(w io.Writer, r runstore.Run) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	row := func(k, v string) { fmt.Fprintf(tw, "%s:\t%s\n", k, v) }
	row("ID", r.ID)
	row("Experiment", r.Namespace+"/"+r.Name)
	row("Fault", r.FaultType)
	row("Result", r.Result)
	if r.Message != "" {
		row("Message", r.Message)
	}
	row("Selector", formatSelector(r.Selector))
	row("Started", r.StartedAt.UTC().Format(time.RFC3339))
	if r.InjectedAt != nil {
		row("Injected", r.InjectedAt.UTC().Format(time.RFC3339))
	}
	if r.RecoveredAt != nil {
		row("Recovered", r.RecoveredAt.UTC().Format(time.RFC3339))
	}
	row("Baseline ready pods", fmt.Sprint(r.BaselineReadyPods))
	if len(r.AffectedPods) > 0 {
		row("Affected pods", strings.Join(r.AffectedPods, ", "))
	}
	row("Pod recovery", millis(r.PodRecoveryMillis))
	if r.SteadyStateURL == "" {
		row("User recovery", "- (no steady-state probe)")
	} else {
		row("Steady-state URL", r.SteadyStateURL)
		row("User recovery", millis(r.UserRecoveryMillis))
		row("Users impacted", impacted(r))
		row("Probes", fmt.Sprintf("%d passed, %d failed", r.ProbeSuccesses, r.ProbeFailures))
		if a, ok := r.Availability(); ok {
			row("Availability", fmt.Sprintf("%.1f%%", a*100))
		}
	}
	if !r.RecordedAt.IsZero() {
		row("Recorded", r.RecordedAt.UTC().Format(time.RFC3339))
	}
	_ = tw.Flush()
}

func runReport(args []string, d Deps) error {
	fs, uri := newFlags("report", d)
	ff := addFilterFlags(fs, "7d")
	format := fs.String("format", "text", "output format: text, markdown or json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *format != "text" && *format != "markdown" && *format != "json" {
		return fmt.Errorf("invalid --format %q: use text, markdown or json", *format)
	}
	filter, err := ff.build(d.Now())
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	store, closeFn, err := connect(ctx, d, *uri)
	if err != nil {
		return err
	}
	defer closeFn()

	runs, err := store.List(ctx, filter)
	if err != nil {
		return err
	}
	s := report.Summarize(runs)
	switch *format {
	case "json":
		return writeJSON(d.Stdout, s)
	case "markdown":
		fmt.Fprint(d.Stdout, s.Markdown())
	default:
		fmt.Fprint(d.Stdout, s.Text())
	}
	return nil
}

func millis(v *int64) string {
	if v == nil {
		return "-"
	}
	return report.FormatMillis(*v)
}

func impacted(r runstore.Run) string {
	if r.SteadyStateURL == "" {
		return "-"
	}
	if r.UserImpacted {
		return "yes"
	}
	return "no"
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func formatSelector(sel map[string]string) string {
	if len(sel) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(sel))
	for k, v := range sel {
		parts = append(parts, k+"="+v)
	}
	// Deterministic output.
	for i := 1; i < len(parts); i++ {
		for j := i; j > 0 && parts[j] < parts[j-1]; j-- {
			parts[j], parts[j-1] = parts[j-1], parts[j]
		}
	}
	return strings.Join(parts, ",")
}
