package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"modernc.org/sqlite"
	"modernc.org/sqlite/lib"
)

type detail struct {
	Detail string `json:"detail"`
}

func securityHeaders(header http.Header) {
	header.Set("Cache-Control", "no-store")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
}

func writeJSON(writer http.ResponseWriter, status int, body any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(body)
}

func reportBusy(writer http.ResponseWriter, message string) {
	writer.Header().Set("Retry-After", "1")
	writeJSON(writer, http.StatusServiceUnavailable, detail{message})
}

func databaseError(writer http.ResponseWriter, err error) {
	var sqliteError *sqlite.Error
	busy := errors.Is(err, context.DeadlineExceeded)
	if errors.As(err, &sqliteError) {
		switch sqliteError.Code() & 0xff {
		case sqlite3.SQLITE_BUSY, sqlite3.SQLITE_LOCKED, sqlite3.SQLITE_INTERRUPT:
			busy = true
		}
	}
	if busy {
		reportBusy(writer, "数据库繁忙或查询超时，请稍后重试")
	} else {
		writeJSON(writer, http.StatusInternalServerError, detail{"数据库暂时不可用"})
	}
}

func (panel *panel) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	securityHeaders(writer.Header())
	if request.Method != "GET" && request.Method != "HEAD" && request.Method != "OPTIONS" {
		if origin := request.Header.Get("Origin"); origin != "" && origin != panel.baseURL {
			writeJSON(writer, 403, detail{"请求来源不匹配"})
			return
		}
		if media, _, _ := strings.Cut(request.Header.Get("Content-Type"), ";"); media != "application/json" {
			writeJSON(writer, 415, detail{"请使用 JSON 请求"})
			return
		}
		request.Body = http.MaxBytesReader(writer, request.Body, 128<<10)
	}
	panel.mux.ServeHTTP(writer, request)
}

func (panel *panel) session(ctx context.Context, request *http.Request) (string, error) {
	var csrf string
	err := panel.database.QueryRowContext(ctx, "SELECT csrf FROM sessions WHERE token_hash=? AND expires>?",
		sessionHash(request), time.Now().Unix()).Scan(&csrf)
	return csrf, err
}

func (panel *panel) me(writer http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), time.Second)
	defer cancel()
	csrf, err := panel.session(ctx, request)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(writer, http.StatusOK, map[string]bool{"authenticated": false})
		return
	}
	if err != nil {
		databaseError(writer, err)
		return
	}
	var username string
	if err := panel.database.QueryRowContext(ctx, "SELECT username FROM admin WHERE id=1").Scan(&username); err != nil {
		databaseError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"authenticated": true, "csrf": csrf, "username": username, "base_url": strings.TrimRight(panel.baseURL, "/"),
	})
}

type activityQuery struct {
	userID, page, pageSize, beforeID int64
	search                           string
}

func queryValue(values url.Values, key string) string {
	entries := values[key]
	if len(entries) == 0 {
		return ""
	}
	return entries[len(entries)-1]
}

func queryInt(values url.Values, key string, fallback int64) (int64, bool) {
	if !values.Has(key) {
		return fallback, true
	}
	number, err := strconv.ParseInt(queryValue(values, key), 10, 64)
	return number, err == nil
}

func parseActivity(raw string) (activityQuery, bool) {
	values, err := url.ParseQuery(strings.ReplaceAll(raw, ";", "%3B"))
	if err != nil {
		return activityQuery{}, false
	}
	userID, userOK := queryInt(values, "user_id", 0)
	page, pageOK := queryInt(values, "page", 1)
	pageSize, sizeOK := queryInt(values, "page_size", 25)
	beforeID, beforeOK := queryInt(values, "before_id", 0)
	if !userOK || !pageOK || !sizeOK || !beforeOK || page < 1 || pageSize < 1 || pageSize > 100 || (values.Has("before_id") && beforeID < 1) {
		return activityQuery{}, false
	}
	search := []rune(queryValue(values, "search"))
	return activityQuery{userID, page, pageSize, beforeID, string(search[:min(len(search), 253)])}, true
}

type activityItem struct {
	ID      int64  `json:"id"`
	Time    int64  `json:"ts"`
	Domain  string `json:"domain"`
	Port    int64  `json:"port"`
	Network string `json:"network"`
	UserID  int64  `json:"user_id"`
	Name    string `json:"name"`
}

type activityResult struct {
	Items    []activityItem `json:"items"`
	Total    int64          `json:"total"`
	Page     int64          `json:"page"`
	PageSize int64          `json:"page_size"`
	Pages    int64          `json:"pages"`
	BeforeID *int64         `json:"before_id"`
}

func (panel *panel) activity(writer http.ResponseWriter, request *http.Request, query activityQuery) {
	authContext, cancelAuth := context.WithTimeout(request.Context(), time.Second)
	_, err := panel.session(authContext, request)
	cancelAuth()
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(writer, http.StatusUnauthorized, detail{"请先登录"})
		return
	}
	if err != nil {
		databaseError(writer, err)
		return
	}
	select {
	case panel.reportSlots <- struct{}{}:
		defer func() { <-panel.reportSlots }()
	default:
		reportBusy(writer, "查询繁忙，请稍后重试")
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), time.Second)
	defer cancel()
	result, err := panel.readActivity(ctx, query)
	if err != nil {
		databaseError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (panel *panel) readActivity(ctx context.Context, query activityQuery) (activityResult, error) {
	result := activityResult{Items: []activityItem{}, PageSize: query.pageSize}
	tx, err := panel.database.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	where, args := " WHERE e.ts>=?", []any{time.Now().Unix() - 7*86400}
	if query.userID != 0 {
		where += " AND e.user_id=?"
		args = append(args, query.userID)
	}
	if query.search != "" {
		where += " AND e.domain LIKE ?"
		args = append(args, "%"+query.search+"%")
	}
	beforeID := sql.NullInt64{Int64: query.beforeID, Valid: query.beforeID != 0}
	if !beforeID.Valid {
		if err := tx.QueryRowContext(ctx, "SELECT MAX(e.id) FROM events e"+where, args...).Scan(&beforeID); err != nil {
			return result, err
		}
	}
	if beforeID.Valid {
		result.BeforeID = &beforeID.Int64
	}
	where += " AND e.id<=?"
	args = append(args, beforeID.Int64)
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM events e"+where, args...).Scan(&result.Total); err != nil {
		return result, err
	}
	result.Pages = max(1, (result.Total+query.pageSize-1)/query.pageSize)
	result.Page = min(query.page, result.Pages)
	args = append(args, query.pageSize, (result.Page-1)*query.pageSize)
	rows, err := tx.QueryContext(ctx, `SELECT e.id,e.ts,e.domain,e.port,e.network,e.user_id,u.name FROM
		(SELECT e.id,e.ts,e.domain,e.port,e.network,e.user_id FROM events e`+where+`
		ORDER BY e.id DESC LIMIT ? OFFSET ?) e JOIN users u ON u.id=e.user_id ORDER BY e.id DESC`, args...)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var item activityItem
		if err := rows.Scan(&item.ID, &item.Time, &item.Domain, &item.Port, &item.Network, &item.UserID, &item.Name); err != nil {
			return result, err
		}
		result.Items = append(result.Items, item)
	}
	return result, rows.Err()
}
