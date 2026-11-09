package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/OpenVibers/OpenVibe.Link/internal/memorystore"
)

// memoryExportUsage is what "openvibe-link memory export" prints.
const memoryExportUsage = "openvibe-link memory export [--out FILE] [--scope SCOPE] [--force]\n" +
	"\n" +
	"  Writes your one portable memory file: a JSON document with every note, its\n" +
	"  kind, scope, source and confidence, and no account id or note id. The same\n" +
	"  file can be imported into another OpenVibe account.\n" +
	"\n" +
	"  --out FILE       where to write it (default openvibe-memory-YYYY-MM-DD.json;\n" +
	"                   - writes to stdout)\n" +
	"  --scope SCOPE    only one scope: user or project:<slug>\n" +
	"  --force          replace the file if it already exists\n" +
	"  --endpoint URL   the store (default " + memorystore.DefaultBaseURL + ")\n" +
	"  --token-env NAME the variable the access token is read from (default " + memorystore.DefaultTokenEnv + ")\n"

// memoryImportUsage is what "openvibe-link memory import" prints.
const memoryImportUsage = "openvibe-link memory import --file FILE [--dry-run] [--yes]\n" +
	"\n" +
	"  Reads a portable memory file back into your store. The store validates the\n" +
	"  format, removes secrets again and merges a note that says the same thing as\n" +
	"  one you already have; it never copies the same text twice.\n" +
	"\n" +
	"  --file FILE      the file to import (- reads stdin)\n" +
	"  --dry-run        show how many notes the file holds, and send nothing\n" +
	"  --yes            do not ask before importing\n" +
	"  --endpoint URL   the store (default " + memorystore.DefaultBaseURL + ")\n" +
	"  --token-env NAME the variable the access token is read from (default " + memorystore.DefaultTokenEnv + ")\n"

// memoryPortable is the part of the portable format this command reads for its
// summary: the format id, its version, and the notes.
type memoryPortable struct {
	Format     string            `json:"format"`
	Version    int               `json:"version"`
	ExportedAt string            `json:"exported_at"`
	Items      []json.RawMessage `json:"items"`
}

// cmdMemoryExport writes the one portable memory file.
func cmdMemoryExport(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("memory export", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, memoryExportUsage) }
	out := fs.String("out", "", "where to write the file")
	scope := fs.String("scope", "", "only one scope")
	force := fs.Bool("force", false, "replace an existing file")
	endpoint := fs.String("endpoint", memorystore.DefaultBaseURL, "the memory store")
	tokenEnv := fs.String("token-env", memorystore.DefaultTokenEnv, "the variable the access token is read from")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "unknown argument %q\n\n", fs.Arg(0))
		fmt.Fprint(stderr, memoryExportUsage)
		return 2
	}
	token := strings.TrimSpace(os.Getenv(*tokenEnv))
	if token == "" {
		fmt.Fprintf(stderr, "error: %s is not set: it must hold your OpenVibe access token\n", *tokenEnv)
		return 1
	}
	client, err := memorystore.New(memorystore.Options{BaseURL: *endpoint, Token: token})
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	doc, err := client.Export(context.Background(), *scope)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	var portable memoryPortable
	if err := json.Unmarshal(doc, &portable); err != nil {
		fmt.Fprintln(stderr, "error: the store answered something that is not a portable file")
		return 1
	}
	target := *out
	if target == "" {
		target = "openvibe-memory-" + time.Now().Format("2006-01-02") + ".json"
	}
	if target == "-" {
		if _, err := stdout.Write(doc); err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return 1
		}
		fmt.Fprintf(stderr, "Wrote %d notes to stdout.\n", len(portable.Items))
		return 0
	}
	if !*force {
		if _, err := os.Stat(target); err == nil {
			fmt.Fprintf(stderr, "error: %s already exists; pass --force to replace it\n", target)
			return 1
		}
	}
	// The file is the person's own memory: only they may read it.
	if err := os.WriteFile(target, doc, 0o600); err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	fmt.Fprintf(stdout, "Wrote %d notes to %s.\n", len(portable.Items), target)
	return 0
}

// cmdMemoryImport reads a portable memory file back into the store.
func cmdMemoryImport(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("memory import", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, memoryImportUsage) }
	file := fs.String("file", "-", "the file to import")
	dryRun := fs.Bool("dry-run", false, "show the count and send nothing")
	assumeYes := fs.Bool("yes", false, "do not ask before importing")
	endpoint := fs.String("endpoint", memorystore.DefaultBaseURL, "the memory store")
	tokenEnv := fs.String("token-env", memorystore.DefaultTokenEnv, "the variable the access token is read from")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "unknown argument %q\n\n", fs.Arg(0))
		fmt.Fprint(stderr, memoryImportUsage)
		return 2
	}
	var raw []byte
	var err error
	if *file == "-" {
		raw, err = io.ReadAll(io.LimitReader(stdin, 64<<20))
	} else {
		raw, err = os.ReadFile(*file)
	}
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	var portable memoryPortable
	if err := json.Unmarshal(raw, &portable); err != nil || portable.Format == "" {
		fmt.Fprintln(stderr, "error: that is not a portable memory file")
		return 1
	}
	if portable.Format != "openvibe.memory-export@1" || portable.Version != 1 {
		fmt.Fprintf(stderr, "error: the file is %s version %d; this Node reads openvibe.memory-export@1 version 1\n", portable.Format, portable.Version)
		return 1
	}
	fmt.Fprintf(stdout, "Portable memory file: %d notes, exported %s.\n", len(portable.Items), portable.ExportedAt)
	if *dryRun {
		fmt.Fprintln(stdout, "Nothing was sent: this was a dry run.")
		return 0
	}
	if len(portable.Items) == 0 {
		fmt.Fprintln(stdout, "Nothing to import.")
		return 0
	}
	if !*assumeYes && !confirmed(stdin, stdout, len(portable.Items)) {
		fmt.Fprintln(stdout, "\nNothing was imported.")
		return 0
	}
	token := strings.TrimSpace(os.Getenv(*tokenEnv))
	if token == "" {
		fmt.Fprintf(stderr, "error: %s is not set: it must hold your OpenVibe access token\n", *tokenEnv)
		return 1
	}
	client, err := memorystore.New(memorystore.Options{BaseURL: *endpoint, Token: token})
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	res, err := client.Import(context.Background(), bytes.TrimSpace(raw))
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	fmt.Fprintf(stdout, "\nImported: %d new, %d already there, %d skipped.\n", res.Created, res.Deduplicated, res.Skipped)
	return 0
}
