package aql

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"

	"relic/internal/artifacts/itemquery"
)

// TimeFormat is how AQL renders timestamps.
const TimeFormat = "2006-01-02T15:04:05.000Z07:00"

// ItemRow is one result row.
type ItemRow = itemquery.Row

// Items runs a plan and calls fn for each row, stopping at the first error.
func Items(ctx context.Context, db itemquery.Querier, plan *Plan, fn func(ItemRow) error) error {
	return itemquery.Run(ctx, db, plan.compiled, fn)
}

// WriteItems streams an items plan as an AQL JSON response.
func WriteItems(ctx context.Context, db itemquery.Querier, plan *Plan, w io.Writer, keep func(ItemRow) bool) error {
	bw := bufio.NewWriterSize(w, 32<<10)
	if _, err := bw.WriteString(`{"results":[`); err != nil {
		return err
	}

	var n int64

	err := Items(ctx, db, plan, func(r ItemRow) error {
		if keep != nil && !keep(r) {
			return nil
		}

		if n > 0 {
			if err := bw.WriteByte(','); err != nil {
				return err
			}
		}

		n++

		return encodeItem(bw, plan, r)
	})
	if err != nil {
		return err
	}

	rng := fmt.Sprintf(`],"range":{"start_pos":%d,"end_pos":%d,"total":%d`, plan.Offset, plan.Offset+n, n)
	if plan.Limit != nil {
		rng += `,"limit":` + strconv.FormatInt(*plan.Limit, 10)
	}

	if _, err := bw.WriteString(rng + "}}\n"); err != nil {
		return err
	}

	return bw.Flush()
}

func encodeItem(w *bufio.Writer, plan *Plan, r ItemRow) error {
	obj := make([]byte, 0, 512)
	obj = append(obj, '{')
	first := true
	add := func(key string, v any) {
		if !first {
			obj = append(obj, ',')
		}

		first = false
		obj = strconv.AppendQuote(obj, key)
		obj = append(obj, ':')
		b, _ := json.Marshal(v)
		obj = append(obj, b...)
	}

	for _, f := range plan.Fields {
		switch f {
		case "repo":
			if plan.ShowRepo != nil {
				add(f, plan.ShowRepo(r.Repository))
			} else {
				add(f, r.Repository)
			}
		case "path":
			add(f, r.Path)
		case "name":
			add(f, r.Name)
		case "type":
			add(f, r.Type)
		case "size":
			add(f, r.Size)
		case "created":
			add(f, r.Created.UTC().Format(TimeFormat))
		case "created_by":
			add(f, r.CreatedBy)
		case "modified":
			add(f, r.Modified.UTC().Format(TimeFormat))
		case "modified_by":
			add(f, r.ModifiedBy)
		case "updated":
			add(f, r.Updated.UTC().Format(TimeFormat))
		case "actual_sha1", "original_sha1":
			if r.SHA1 != nil {
				add(f, *r.SHA1)
			}
		case "actual_md5", "original_md5":
			if r.MD5 != nil {
				add(f, *r.MD5)
			}
		case "sha256":
			if r.SHA256 != nil {
				add(f, *r.SHA256)
			}
		case "depth":
			add(f, r.Depth())
		}
	}

	if plan.Properties && len(r.Properties) > 0 {
		add("properties", r.Properties)
	}

	obj = append(obj, '}')
	_, err := w.Write(obj)

	return err
}
