package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lukastk/sesh/internal/blobs"
	"github.com/lukastk/sesh/internal/config"
)

// runBlob implements `sesh blob <add|ls|get|rm|path|expand>` — the content-addressed
// file store. Files are referenced from prompts by an @blob(<hex>) token (printed by
// `blob add`) and expanded to absolute paths on send/copy. `--machine` routes every
// op to a peer's store, exactly like tickets.
func runBlob(cfg config.Config, args []string) error {
	if len(args) == 0 {
		return printGroupHelp("blob")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "add":
		return blobAdd(cfg, rest)
	case "ls", "list":
		return blobList(cfg, rest)
	case "get":
		return blobGet(cfg, rest)
	case "rm", "delete":
		return blobRm(cfg, rest)
	case "path":
		return blobPath(cfg, rest)
	case "expand":
		return blobExpand(cfg, rest)
	default:
		return fmt.Errorf("unknown blob subcommand %q", sub)
	}
}

func blobAdd(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	stdin := fs.Bool("stdin", false, "read the bytes from stdin (requires --name)")
	name := fs.String("name", "", "stored filename (default: the basename of <path>; required with --stdin)")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var data []byte
	var err error
	if *stdin {
		if *name == "" {
			return errors.New("blob add --stdin: --name is required")
		}
		if data, err = io.ReadAll(os.Stdin); err != nil {
			return err
		}
	} else {
		path := fs.Arg(0)
		if path == "" {
			return errors.New("blob add: a file <path> (or --stdin) is required")
		}
		if data, err = os.ReadFile(path); err != nil {
			return err
		}
		if *name == "" {
			*name = filepath.Base(path)
		}
	}
	c := daemonClient(cfg)
	resp, err := c.BlobAdd(context.Background(), *name, data)
	if err != nil {
		return err
	}
	if *asJSON {
		return emitJSON(resp.Blob)
	}
	// Print the TOKEN you paste into a prompt (the short prefix), then the details.
	fmt.Printf("@blob(%s)\n", resp.Blob.Hash[:blobs.TokenPrefixLen])
	fmt.Fprintf(os.Stderr, "stored %s (%s, %d bytes)\n", resp.Blob.Name, resp.Blob.Hash[:blobs.TokenPrefixLen], resp.Blob.Size)
	return nil
}

func blobList(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("ls", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON (one blob per line)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	resp, err := daemonClient(cfg).BlobList(context.Background())
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		for _, b := range resp.Blobs {
			if err := enc.Encode(b); err != nil {
				return err
			}
		}
		return nil
	}
	for _, b := range resp.Blobs {
		fmt.Printf("@blob(%s)\t%8d\t%s\n", b.Hash[:blobs.TokenPrefixLen], b.Size, b.Name)
	}
	return nil
}

func blobGet(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("get", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	hash := fs.Arg(0)
	if hash == "" {
		return errors.New("blob get: a <hash> (or prefix) is required")
	}
	_, data, err := daemonClient(cfg).BlobGet(context.Background(), hash)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(data) // raw bytes, for piping
	return err
}

func blobRm(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("rm", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	hash := fs.Arg(0)
	if hash == "" {
		return errors.New("blob rm: a <hash> (or prefix) is required")
	}
	if err := daemonClient(cfg).BlobDelete(context.Background(), hash); err != nil {
		return err
	}
	fmt.Println("deleted", hash)
	return nil
}

func blobPath(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("path", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	hash := fs.Arg(0)
	if hash == "" {
		return errors.New("blob path: a <hash> (or prefix) is required")
	}
	resp, err := daemonClient(cfg).BlobPath(context.Background(), hash)
	if err != nil {
		return err
	}
	fmt.Println(resp.Path)
	return nil
}

// blobExpand is the stdin→stdout filter: every @blob(<hex>) becomes the blob's
// absolute path ON THE TARGETED DAEMON (--machine). A reference to an unknown blob
// is a loud error (the daemon refuses), never a silent passthrough.
func blobExpand(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("expand", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	text, err := io.ReadAll(os.Stdin)
	if err != nil {
		return err
	}
	resp, err := daemonClient(cfg).BlobExpand(context.Background(), string(text))
	if err != nil {
		return err
	}
	fmt.Print(resp.Text)
	return nil
}
