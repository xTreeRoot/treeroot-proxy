package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	hg "treeroot/gopanel/homegateway"
)

const homeProxySchema = `CREATE TABLE IF NOT EXISTS home_proxy_users(gateway_id TEXT PRIMARY KEY REFERENCES home_gateways(id),user_id INTEGER NOT NULL UNIQUE REFERENCES users(id));`

func gatewayProxyUser(ctx context.Context, db querier, id string) (row, error) {
	return one(ctx, db, `SELECT u.id,u.name,u.enabled,EXISTS(SELECT 1 FROM user_archives a WHERE a.user_id=u.id) archived FROM home_proxy_users p JOIN users u ON u.id=p.user_id WHERE p.gateway_id=?`, id)
}
func gatewayNodes(ctx context.Context, db querier, id string) ([]hg.Node, error) {
	user, e := gatewayProxyUser(ctx, db, id)
	if e == nil {
		if user.number("enabled") != 1 || user.number("archived") != 0 {
			return []hg.Node{}, nil
		}
		return []hg.Node{{ID: fmt.Sprint("user-", user.number("id")), Name: user.text("name")}}, nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return nil, e
	}
	rows, e := query(ctx, db, `SELECT u.id,u.name FROM users u LEFT JOIN user_archives a ON a.user_id=u.id WHERE u.enabled=1 AND a.user_id IS NULL AND NOT EXISTS(SELECT 1 FROM home_proxy_users p WHERE p.user_id=u.id) ORDER BY u.id`)
	if e != nil {
		return nil, e
	}
	nodes := []hg.Node{}
	for _, u := range rows {
		nodes = append(nodes, hg.Node{ID: fmt.Sprint("user-", u.number("id")), Name: u.text("name")})
	}
	return nodes, nil
}
func (app *panel) addGatewayProxy(ctx context.Context, tx *sql.Tx, id, name string, change *proxyUserChange) (row, error) {
	if existing, e := gatewayProxyUser(ctx, tx, id); e == nil {
		return existing, nil
	} else if !errors.Is(e, sql.ErrNoRows) {
		return nil, e
	}
	user, e := insertProxyUser(ctx, tx, "家庭 · "+name)
	if e != nil {
		return nil, e
	}
	if _, e = tx.Exec("INSERT INTO home_proxy_users VALUES(?,?)", id, user.number("id")); e != nil {
		return nil, e
	}
	if e = app.registerProxyUser(ctx, tx, user, change); e != nil {
		return nil, e
	}
	if _, e = tx.Exec("INSERT INTO audit(ts,action,user_id) VALUES(?,'create_home_proxy_user',?)", unixNow(), user.number("id")); e != nil {
		return nil, e
	}
	return user, nil
}
func (app *panel) homeEnsureProxy(_ http.ResponseWriter, r *http.Request) (any, error) {
	var change proxyUserChange
	var user row
	id := r.PathValue("id")
	e := app.submit(writeJob{ctx: r.Context(), run: func(tx *sql.Tx) error {
		g, e := homeRow(r.Context(), tx, id)
		if e != nil {
			return e
		}
		if existing, e := gatewayProxyUser(r.Context(), tx, id); e == nil {
			user = existing
			return nil
		} else if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		user, e = app.addGatewayProxy(r.Context(), tx, id, g.text("name"), &change)
		if e != nil {
			return e
		}
		c, e := homeConfig(r.Context(), tx, id, g.number("revision"))
		if e != nil {
			return e
		}
		node := hg.Node{ID: fmt.Sprint("user-", user.number("id")), Name: user.text("name")}
		c.Nodes = []hg.Node{node}
		c.DefaultNode = node.ID
		for i := range c.Rules {
			if c.Rules[i].Action == "proxy" {
				c.Rules[i].Node = node.ID
			}
		}
		if e = app.saveHomeConfig(r.Context(), tx, id, c); e != nil {
			return e
		}
		return homeAudit(tx, id, "proxy-account", fmt.Sprint(user.number("id")))
	}, rollback: func() { app.rollbackProxyUser(&change) }})
	if e != nil {
		return nil, e
	}
	return row{"user_id": user.number("id"), "name": user.text("name")}, nil
}
