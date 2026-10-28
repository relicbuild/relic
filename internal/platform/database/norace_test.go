//go:build !race

package database

import "time"

const listItemsAtDeadline = 3 * time.Second
