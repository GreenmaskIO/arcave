package api

import (
	"time"
)

type ProjectListItem struct {
	Name      string
	CreatedAt time.Time
	Sessions  int
}

type ProjectListResponse struct {
	Projects []ProjectListItem
}
