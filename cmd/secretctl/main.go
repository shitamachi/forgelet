// Command secretctl manages sealed secrets via the control-plane API (spec 0003 T7).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	switch cmd {
	case "set":
		fs := flag.NewFlagSet("set", flag.ExitOnError)
		server := fs.String("server", "http://localhost:8080", "control plane base URL")
		scope := fs.String("scope", "repository", "secret scope")
		name := fs.String("name", "", "secret name")
		value := fs.String("value", "", "secret value (or read from stdin if empty and --value-file)")
		valueFile := fs.String("value-file", "", "read value from file")
		fs.Parse(os.Args[2:]) //nolint:errcheck
		if *name == "" {
			fmt.Fprintln(os.Stderr, "secretctl set: --name is required")
			os.Exit(2)
		}
		val := *value
		if *valueFile != "" {
			b, err := os.ReadFile(*valueFile)
			if err != nil {
				fmt.Fprintf(os.Stderr, "read value-file: %v\n", err)
				os.Exit(1)
			}
			val = string(bytes.TrimSpace(b))
		} else if val == "" {
			// Try stdin if not a tty
			if fi, _ := os.Stdin.Stat(); fi != nil && (fi.Mode()&os.ModeCharDevice) == 0 {
				b, _ := io.ReadAll(os.Stdin)
				val = string(bytes.TrimSpace(b))
			}
		}
		if val == "" {
			fmt.Fprintln(os.Stderr, "secretctl set: --value or --value-file or stdin is required")
			os.Exit(2)
		}
		if err := doSet(*server, *scope, *name, val); err != nil {
			fmt.Fprintf(os.Stderr, "set: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("secret %s/%s set\n", *scope, *name)
	case "list":
		fs := flag.NewFlagSet("list", flag.ExitOnError)
		server := fs.String("server", "http://localhost:8080", "control plane base URL")
		fs.Parse(os.Args[2:]) //nolint:errcheck
		if err := doList(*server); err != nil {
			fmt.Fprintf(os.Stderr, "list: %v\n", err)
			os.Exit(1)
		}
	case "delete":
		fs := flag.NewFlagSet("delete", flag.ExitOnError)
		server := fs.String("server", "http://localhost:8080", "control plane base URL")
		scope := fs.String("scope", "repository", "secret scope")
		name := fs.String("name", "", "secret name")
		fs.Parse(os.Args[2:]) //nolint:errcheck
		if *name == "" {
			fmt.Fprintln(os.Stderr, "secretctl delete: --name is required")
			os.Exit(2)
		}
		if err := doDelete(*server, *scope, *name); err != nil {
			fmt.Fprintf(os.Stderr, "delete: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("secret %s/%s deleted\n", *scope, *name)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", cmd)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "Usage: secretctl <set|list|delete> [flags]")
	fmt.Fprintln(os.Stderr, "  set    --scope S --name N --value V|--value-file F [--server URL]")
	fmt.Fprintln(os.Stderr, "  list   [--server URL]")
	fmt.Fprintln(os.Stderr, "  delete --scope S --name N [--server URL]")
}

func doSet(server, scope, name, value string) error {
	body, _ := json.Marshal(map[string]string{"scope": scope, "name": name, "value": value})
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server+"/api/secrets", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

func doList(server string) error {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server+"/api/secrets", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(b))
	}
	var list []struct {
		Scope string `json:"scope"`
		Name  string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return err
	}
	for _, s := range list {
		fmt.Printf("%s/%s\n", s.Scope, s.Name)
	}
	return nil
}

func doDelete(server, scope, name string) error {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodDelete, fmt.Sprintf("%s/api/secrets/%s/%s", server, scope, name), nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(b))
	}
	return nil
}
