//go:build race

package database

import "time"

// The race detector slows the SQLite engine, which is Go, by about a hundred times.
const listItemsAtDeadline = 60 * time.Second
