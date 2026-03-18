package tools

// Pagination provides context for list/query results.
// Count, TotalAvailable, and Limit never use omitempty so zero values are visible.
type Pagination struct {
	Count          int  `json:"count"`
	TotalAvailable int  `json:"total_available"`
	Limit          int  `json:"limit"`
	Filtered       bool `json:"filtered,omitempty"`
}

// NewPagination creates a Pagination with all fields set.
func NewPagination(count, totalAvailable, limit int, filtered bool) Pagination {
	return Pagination{
		Count:          count,
		TotalAvailable: totalAvailable,
		Limit:          limit,
		Filtered:       filtered,
	}
}
