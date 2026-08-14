package api

type ProviderListItem struct {
	ID     string
	Name   string
	Type   string
	Status string
}

type ProviderListResponse struct {
	Providers []ProviderListItem
}
