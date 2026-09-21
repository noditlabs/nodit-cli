package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

type addressQuery struct {
	page, size          int
	sort, order, search string
}

func (a *app) classicAddressesCommand() *cobra.Command {
	root := asGroup(&cobra.Command{Use: "addresses", Short: "Manage Classic ADDRESS_ACTIVITY addresses"})
	root.AddCommand(a.addressListCommand(), a.addressUpdateCommand(), a.addressExportCommand())
	return root
}

func bindAddressQuery(cmd *cobra.Command, q *addressQuery, paged bool) {
	if paged {
		cmd.Flags().IntVar(&q.page, "page", 1, "Page number")
		cmd.Flags().IntVar(&q.size, "size", 100, "Results per page, 1-1000")
	}
	cmd.Flags().StringVar(&q.sort, "sort", "createdAt", "createdAt or address")
	cmd.Flags().StringVar(&q.order, "order", "asc", "asc or desc")
	cmd.Flags().StringVar(&q.search, "search", "", "Address prefix")
}

func validateAddressQuery(q addressQuery, paged bool) error {
	if paged && (q.page < 1 || q.size < 1 || q.size > 1000) {
		return invalid("--page must be positive and --size must be 1-1000.")
	}
	if !inWords("createdAt address", q.sort) || !inWords("asc desc", q.order) {
		return invalid("--sort must be createdAt or address, and --order must be asc or desc.")
	}
	return nil
}

func addAddressQuery(u *url.URL, q addressQuery, paged bool) {
	v := u.Query()
	if paged {
		v.Set("page", fmt.Sprint(q.page))
		v.Set("size", fmt.Sprint(q.size))
	}
	v.Set("sort", q.sort)
	v.Set("order", q.order)
	if q.search != "" {
		v.Set("search", q.search)
	}
	u.RawQuery = v.Encode()
}

func (a *app) addressListCommand() *cobra.Command {
	var flags productFlags
	var q addressQuery
	cmd := &cobra.Command{Use: "list <id>", Short: "List ADDRESS_ACTIVITY addresses", Args: helpOnNoArgs(cobra.ExactArgs(1)), RunE: func(cmd *cobra.Command, args []string) error {
		if err := validID(args[0]); err != nil {
			return err
		}
		if err := validateAddressQuery(q, true); err != nil {
			return err
		}
		n, err := a.productNetwork(flags.network, "webhook")
		if err != nil {
			return err
		}
		key, err := a.apiKey(flags.apiKey)
		if err != nil {
			return err
		}
		u, _ := url.Parse(a.webhookEndpoint(n, classicWebhook, "/"+args[0]+"/addresses"))
		addAddressQuery(u, q, true)
		result, _, err := a.apiRequest(cmd.Context(), http.MethodGet, u.String(), key, nil)
		if err != nil {
			return err
		}
		return a.success(result)
	}}
	flags.bind(cmd)
	bindAddressQuery(cmd, &q, true)
	return cmd
}

func (a *app) addressUpdateCommand() *cobra.Command {
	var flags webhookFlags
	cmd := &cobra.Command{Use: "update <id>", Short: "Add and remove ADDRESS_ACTIVITY addresses", Args: helpOnNoArgs(cobra.ExactArgs(1)), RunE: func(cmd *cobra.Command, args []string) error {
		if !cmd.Flags().Changed("network") {
			return invalid("Address update requires an explicit --network.")
		}
		if err := validID(args[0]); err != nil {
			return err
		}
		raw, removeCount, err := validateAddressPatch(flags.body)
		if err != nil {
			return err
		}
		if removeCount > 0 {
			if err = a.confirm(fmt.Sprintf("Remove %d address entries?", removeCount), flags.yes); err != nil {
				return err
			}
		}
		n, err := a.productNetwork(flags.network, "webhook")
		if err != nil {
			return err
		}
		key, err := a.apiKey(flags.apiKey)
		if err != nil {
			return err
		}
		result, _, err := a.apiRequest(cmd.Context(), http.MethodPatch, a.webhookEndpoint(n, classicWebhook, "/"+args[0]+"/addresses"), key, raw)
		if err != nil {
			return err
		}
		return a.success(result)
	}}
	flags.bind(cmd)
	cmd.Flags().StringVar(&flags.body, "body", "", "Required JSON object or @file with add/remove arrays")
	cmd.Flags().BoolVarP(&flags.yes, "yes", "y", false, "Skip removal confirmation")
	_ = cmd.MarkFlagRequired("body")
	return cmd
}

func validateAddressPatch(value string) (json.RawMessage, int, error) {
	raw, err := readJSONBody(value)
	if err != nil {
		return nil, 0, err
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(raw, &body) != nil || body == nil {
		return nil, 0, invalid("Address body must be a JSON object with add and/or remove arrays.")
	}
	for name := range body {
		if !inWords("add remove", name) {
			return nil, 0, invalid("Address body supports only add and remove.")
		}
	}
	total, removes := 0, 0
	for _, name := range []string{"add", "remove"} {
		if field, ok := body[name]; ok {
			var values []string
			if json.Unmarshal(field, &values) != nil {
				return nil, 0, invalid(name + " must be an array of strings.")
			}
			for _, value := range values {
				if strings.TrimSpace(value) == "" || strings.IndexFunc(value, func(r rune) bool { return r <= ' ' || r >= 127 }) >= 0 {
					return nil, 0, invalid(name + " contains an invalid address.")
				}
			}
			total += len(values)
			if name == "remove" {
				removes = len(values)
			}
		}
	}
	if total == 0 {
		return nil, 0, invalid("Address body must contain at least one add or remove entry.")
	}
	if total > 1000 {
		return nil, 0, invalid("The combined add and remove count cannot exceed 1000.")
	}
	return raw, removes, nil
}

func (a *app) addressExportCommand() *cobra.Command {
	var flags productFlags
	var q addressQuery
	var file string
	cmd := &cobra.Command{Use: "export <id>", Short: "Download ADDRESS_ACTIVITY addresses as CSV", Args: helpOnNoArgs(cobra.ExactArgs(1)), RunE: func(cmd *cobra.Command, args []string) error {
		if err := validID(args[0]); err != nil {
			return err
		}
		if err := validateAddressQuery(q, false); err != nil {
			return err
		}
		if strings.TrimSpace(file) == "" {
			return invalid("--file is required.")
		}
		n, err := a.productNetwork(flags.network, "webhook")
		if err != nil {
			return err
		}
		key, err := a.apiKey(flags.apiKey)
		if err != nil {
			return err
		}
		u, _ := url.Parse(a.webhookEndpoint(n, classicWebhook, "/"+args[0]+"/addresses/download"))
		addAddressQuery(u, q, false)
		if err = a.downloadCSV(cmd.Context(), u.String(), key, file); err != nil {
			return err
		}
		return a.success(map[string]any{"file": file, "format": "csv"})
	}}
	flags.bind(cmd)
	bindAddressQuery(cmd, &q, false)
	cmd.Flags().StringVar(&file, "file", "", "Required new CSV file path")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}

func (a *app) downloadCSV(ctx context.Context, endpoint, key, target string) error {
	abs, err := filepath.Abs(target)
	if err != nil {
		return invalid("Cannot resolve the CSV file path.")
	}
	if _, err = os.Lstat(abs); err == nil {
		return failure("FILE_EXISTS", "CSV target already exists. Pass a new path with --file.")
	} else if !errors.Is(err, os.ErrNotExist) {
		return failure("FILE_WRITE_FAILED", "Cannot inspect the CSV target.")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return invalid("Cannot create CSV request.")
	}
	req.Header.Set("X-API-KEY", key)
	req.Header.Set("Accept", "text/csv")
	req.Header.Set("User-Agent", userAgent())
	client := *a.httpClient
	client.Timeout = time.Duration(a.timeoutMS) * time.Millisecond
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return failure("API_REQUEST_FAILED", "CSV download failed.")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		content, _ := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponseBytes))
		var value any
		d := json.NewDecoder(bytes.NewReader(content))
		d.UseNumber()
		_ = d.Decode(&value)
		return apiFailure(resp.StatusCode, redactAPIValue(value, key), resp.Header)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(strings.ToLower(ct), "text/csv") {
		return failure("INVALID_API_RESPONSE", "Address export did not return CSV.")
	}
	tmp, err := os.CreateTemp(filepath.Dir(abs), ".nodit-addresses-*.csv")
	if err != nil {
		return failure("FILE_WRITE_FAILED", "Cannot create the CSV file.")
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	_ = tmp.Chmod(0600)
	_, copyErr := io.Copy(tmp, resp.Body)
	if copyErr == nil {
		copyErr = tmp.Sync()
	}
	if closeErr := tmp.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		return failure("FILE_WRITE_FAILED", "Cannot finish the CSV download.")
	}
	if err = os.Link(tmpName, abs); err != nil {
		if errors.Is(err, os.ErrExist) {
			return failure("FILE_EXISTS", "CSV target already exists. Pass a new path with --file.")
		}
		return failure("FILE_WRITE_FAILED", "Cannot finalize the CSV file.")
	}
	return nil
}
