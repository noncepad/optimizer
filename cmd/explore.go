package main

import (
	"database/sql"
	"encoding/hex"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	sgo "github.com/gagliardetto/solana-go"
)

// exploreRowsPerPage caps how many rows a single /explore/table/<name> page
// shows -- raydium_amm_pool alone has 700k+ rows, so this is a hard limit,
// not a default that can be raised via a query param.
const exploreRowsPerPage = 50

// prefetchExplorerPrefix and portfolioExplorerPrefix are the two explorer
// mounts registered by DashboardCmd.Run -- one per SQLite file the
// dashboard reads from. pnl.go's drill-down links target the portfolio
// one directly since PnL rows only ever link into balance_snapshot.
const (
	prefetchExplorerPrefix  = "/explore"
	portfolioExplorerPrefix = "/portfolio-explore"
)

// registerExploreRoutes wires up a generic, read-only table browser -- an
// index of every table in db, and a paginated/sortable/filterable row view
// per table -- mounted at prefix (e.g. "/explore"). It only ever runs
// SELECT/PRAGMA -- no writes. Since this dashboard reads from two separate
// SQLite files (prefetch.db and portfolio.db), it's called once per
// database with a distinct prefix so both get their own browsable routes
// without table names colliding.
func registerExploreRoutes(mux *http.ServeMux, prefix string, db *sql.DB) {
	mux.HandleFunc(prefix, func(w http.ResponseWriter, req *http.Request) {
		data, err := buildExploreIndex(db, prefix)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := exploreIndexTemplate.Execute(w, data); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})
	tablePrefix := prefix + "/table/"
	mux.HandleFunc(tablePrefix, func(w http.ResponseWriter, req *http.Request) {
		table := strings.TrimPrefix(req.URL.Path, tablePrefix)
		data, err := buildExploreTable(db, prefix, table, req.URL.Query())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := exploreTableTemplate.Execute(w, data); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})
}

func exploreTables(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

type exploreIndexRow struct {
	Table string
	Rows  int
}

type exploreIndexData struct {
	BasePath string
	Tables   []exploreIndexRow
}

func buildExploreIndex(db *sql.DB, prefix string) (*exploreIndexData, error) {
	tables, err := exploreTables(db)
	if err != nil {
		return nil, err
	}
	out := &exploreIndexData{BasePath: prefix, Tables: make([]exploreIndexRow, 0, len(tables))}
	for _, t := range tables {
		n, err := count(db, t)
		if err != nil {
			return nil, err
		}
		out.Tables = append(out.Tables, exploreIndexRow{Table: t, Rows: n})
	}
	return out, nil
}

// exploreColumn is one column of a table, as reported by PRAGMA table_info.
type exploreColumn struct {
	Name   string
	IsBlob bool // BLOB columns get base58-decoded on display and filter input
}

func exploreColumns(db *sql.DB, table string) ([]exploreColumn, error) {
	rows, err := db.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return nil, fmt.Errorf("table_info %s: %w", table, err)
	}
	defer func() {
		_ = rows.Close()
	}()
	var out []exploreColumn
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return nil, err
		}
		out = append(out, exploreColumn{Name: name, IsBlob: strings.Contains(strings.ToUpper(colType), "BLOB")})
	}
	return out, rows.Err()
}

type exploreTableData struct {
	BasePath   string
	Table      string
	Columns    []string
	Rows       [][]string
	Page       int
	TotalPages int
	Total      int
	SortCol    string
	SortDir    string
	FilterCol  string
	FilterVal  string
	FilterCols []string
	PrevURL    string
	NextURL    string
	HasPrev    bool
	HasNext    bool
}

func buildExploreTable(db *sql.DB, prefix, table string, q url.Values) (*exploreTableData, error) {
	valid, err := exploreTables(db)
	if err != nil {
		return nil, err
	}
	present := false
	for _, t := range valid {
		if t == table {
			present = true
			break
		}
	}
	if !present {
		return nil, fmt.Errorf("no such table: %s", table)
	}
	cols, err := exploreColumns(db, table)
	if err != nil {
		return nil, err
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("table %s has no columns", table)
	}
	colByName := make(map[string]exploreColumn, len(cols))
	colNames := make([]string, len(cols))
	for i, c := range cols {
		colByName[c.Name] = c
		colNames[i] = c.Name
	}

	sortCol := q.Get("sort")
	if _, ok := colByName[sortCol]; !ok {
		sortCol = colNames[0]
	}
	sortDir := "asc"
	if strings.EqualFold(q.Get("dir"), "desc") {
		sortDir = "desc"
	}

	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}

	filterCol := q.Get("col")
	filterVal := q.Get("val")
	var whereClause string
	var args []interface{}
	if fc, ok := colByName[filterCol]; ok && filterVal != "" {
		whereClause = fmt.Sprintf(" WHERE %s = ?", filterCol)
		args = append(args, filterArgValue(fc, filterVal))
	} else {
		filterCol = ""
	}

	var total int
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM %s%s", table, whereClause)
	if err := db.QueryRow(countQuery, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("count %s: %w", table, err)
	}
	totalPages := (total + exploreRowsPerPage - 1) / exploreRowsPerPage
	if totalPages < 1 {
		totalPages = 1
	}
	if page > totalPages {
		page = totalPages
	}
	offset := (page - 1) * exploreRowsPerPage

	selectQuery := fmt.Sprintf("SELECT * FROM %s%s ORDER BY %s %s LIMIT ? OFFSET ?",
		table, whereClause, sortCol, strings.ToUpper(sortDir))
	rowArgs := append(append([]interface{}{}, args...), exploreRowsPerPage, offset)
	rows, err := db.Query(selectQuery, rowArgs...)
	if err != nil {
		return nil, fmt.Errorf("query %s: %w", table, err)
	}
	defer func() {
		_ = rows.Close()
	}()
	outRows := make([][]string, 0, exploreRowsPerPage)
	for rows.Next() {
		vals := make([]interface{}, len(colNames))
		ptrs := make([]interface{}, len(colNames))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("scan %s: %w", table, err)
		}
		rendered := make([]string, len(vals))
		for i, v := range vals {
			rendered[i] = formatCellValue(v)
		}
		outRows = append(outRows, rendered)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	mkURL := func(p int) string {
		v := url.Values{}
		v.Set("page", strconv.Itoa(p))
		v.Set("sort", sortCol)
		v.Set("dir", sortDir)
		if filterCol != "" {
			v.Set("col", filterCol)
			v.Set("val", filterVal)
		}
		return prefix + "/table/" + table + "?" + v.Encode()
	}

	return &exploreTableData{
		BasePath:   prefix,
		Table:      table,
		Columns:    colNames,
		Rows:       outRows,
		Page:       page,
		TotalPages: totalPages,
		Total:      total,
		SortCol:    sortCol,
		SortDir:    sortDir,
		FilterCol:  filterCol,
		FilterVal:  filterVal,
		FilterCols: colNames,
		PrevURL:    mkURL(page - 1),
		NextURL:    mkURL(page + 1),
		HasPrev:    page > 1,
		HasNext:    page < totalPages,
	}, nil
}

// filterArgValue converts a user-typed filter string into what should
// actually be bound against the column: BLOB pubkey columns accept
// base58 (falling back to hex) and get converted to raw bytes; everything
// else is passed through as text and left to SQLite's type affinity.
func filterArgValue(col exploreColumn, val string) interface{} {
	if !col.IsBlob {
		return val
	}
	if pk, err := sgo.PublicKeyFromBase58(val); err == nil {
		return pk.Bytes()
	}
	if b, err := hex.DecodeString(strings.TrimPrefix(val, "0x")); err == nil {
		return b
	}
	return val
}

// formatCellValue renders one scanned column value for display. 32-byte
// BLOBs are assumed to be Solana pubkeys (true of every pubkey/mint column
// in this schema) and shown base58-encoded; other BLOBs fall back to hex.
func formatCellValue(v interface{}) string {
	switch x := v.(type) {
	case nil:
		return ""
	case []byte:
		if len(x) == 32 {
			return sgo.PublicKeyFromBytes(x).String()
		}
		if len(x) == 0 {
			return ""
		}
		return "0x" + hex.EncodeToString(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case string:
		return x
	default:
		return fmt.Sprintf("%v", x)
	}
}

const exploreStyle = `
<style>
  :root {
    color-scheme: light;
    --surface-1: #fcfcfb; --page: #f9f9f7; --text-primary: #0b0b0b; --text-secondary: #52514e;
    --text-muted: #898781; --gridline: #e1e0d9; --series-seq: #2a78d6; --border: rgba(11,11,11,0.10);
    --link: #2a78d6;
  }
  @media (prefers-color-scheme: dark) {
    :root {
      color-scheme: dark;
      --surface-1: #1a1a19; --page: #0d0d0d; --text-primary: #ffffff; --text-secondary: #c3c2b7;
      --text-muted: #898781; --gridline: #2c2c2a; --series-seq: #3987e5; --border: rgba(255,255,255,0.10);
      --link: #3987e5;
    }
  }
  * { box-sizing: border-box; }
  body { margin: 0; padding: 32px; background: var(--page); color: var(--text-primary); font-family: system-ui, -apple-system, "Segoe UI", sans-serif; }
  h1 { font-size: 20px; margin: 0 0 4px; }
  a { color: var(--link); text-decoration: none; }
  a:hover { text-decoration: underline; }
  .meta { color: var(--text-secondary); font-size: 13px; margin-bottom: 20px; }
  .card { background: var(--surface-1); border: 1px solid var(--border); border-radius: 10px; padding: 20px 24px; margin-bottom: 20px; }
  table { width: 100%; border-collapse: collapse; font-size: 13px; }
  th, td { text-align: left; padding: 6px 10px; border-bottom: 1px solid var(--gridline); white-space: nowrap; }
  th { color: var(--text-muted); font-weight: 600; font-size: 11px; text-transform: uppercase; letter-spacing: 0.03em; }
  td.num { text-align: right; font-variant-numeric: tabular-nums; }
  .index-table td:last-child { text-align: right; font-variant-numeric: tabular-nums; }
  .rowbox { overflow-x: auto; }
  form.filter { display: flex; gap: 8px; align-items: center; margin-bottom: 16px; font-size: 13px; }
  form.filter select, form.filter input { font: inherit; padding: 5px 8px; border-radius: 6px; border: 1px solid var(--border); background: var(--surface-1); color: var(--text-primary); }
  .pager { display: flex; gap: 16px; align-items: center; margin-top: 16px; font-size: 13px; color: var(--text-secondary); }
  .pager .disabled { color: var(--text-muted); pointer-events: none; }
</style>`

var exploreIndexTemplate = template.Must(template.New("explore-index").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>{{.BasePath}} explorer</title>` + exploreStyle + `</head>
<body>
  <h1>{{.BasePath}} explorer</h1>
  <div class="meta"><a href="/">&larr; dashboard</a></div>
  <div class="card">
    <table class="index-table">
      <tr><th>Table</th><th>Rows</th></tr>
      {{$base := .BasePath}}
      {{range .Tables}}
      <tr><td><a href="{{$base}}/table/{{.Table}}">{{.Table}}</a></td><td>{{.Rows}}</td></tr>
      {{end}}
    </table>
  </div>
</body></html>
`))

var exploreTableTemplate = template.Must(template.New("explore-table").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>{{.Table}} &middot; {{.BasePath}} explorer</title>` + exploreStyle + `</head>
<body>
  <h1>{{.Table}}</h1>
  <div class="meta"><a href="{{.BasePath}}">&larr; all tables</a> &middot; {{.Total}} rows &middot; page {{.Page}} / {{.TotalPages}}</div>

  <form class="filter" method="get">
    <input type="hidden" name="sort" value="{{.SortCol}}">
    <input type="hidden" name="dir" value="{{.SortDir}}">
    <span>filter</span>
    <select name="col">
      <option value="">(column)</option>
      {{$fc := .FilterCol}}
      {{range .FilterCols}}<option value="{{.}}" {{if eq . $fc}}selected{{end}}>{{.}}</option>{{end}}
    </select>
    <span>=</span>
    <input type="text" name="val" value="{{.FilterVal}}" placeholder="value or base58 pubkey">
    <button type="submit">apply</button>
    {{if .FilterCol}}<a href="{{.BasePath}}/table/{{.Table}}?sort={{.SortCol}}&dir={{.SortDir}}">clear</a>{{end}}
  </form>

  <div class="card rowbox">
    <table>
      <tr>
      {{$base := .BasePath}}
      {{$table := .Table}}
      {{$sortCol := .SortCol}}
      {{$sortDir := .SortDir}}
      {{$filterCol := .FilterCol}}
      {{$filterVal := .FilterVal}}
      {{range .Columns}}
        {{$nextDir := "asc"}}
        {{if and (eq . $sortCol) (eq $sortDir "asc")}}{{$nextDir = "desc"}}{{end}}
        <th><a href="{{$base}}/table/{{$table}}?sort={{.}}&dir={{$nextDir}}{{if $filterCol}}&col={{$filterCol}}&val={{$filterVal}}{{end}}">{{.}}{{if eq . $sortCol}} {{if eq $sortDir "asc"}}&uarr;{{else}}&darr;{{end}}{{end}}</a></th>
      {{end}}
      </tr>
      {{range .Rows}}
      <tr>
        {{range .}}<td>{{.}}</td>{{end}}
      </tr>
      {{end}}
    </table>
  </div>

  <div class="pager">
    <a class="{{if not .HasPrev}}disabled{{end}}" href="{{.PrevURL}}">&larr; prev</a>
    <span>page {{.Page}} / {{.TotalPages}}</span>
    <a class="{{if not .HasNext}}disabled{{end}}" href="{{.NextURL}}">next &rarr;</a>
  </div>
</body></html>
`))
