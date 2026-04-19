package daemon

import (
	"github.com/standardbeagle/agnt/internal/alert"
)

// AlertEntry is an alias for alert.AlertEntry.
// The canonical type lives in internal/alert; this alias keeps daemon-internal
// code readable without a package qualifier.
type AlertEntry = alert.AlertEntry

// AlertStoreFilter is an alias for alert.AlertStoreFilter.
type AlertStoreFilter = alert.AlertStoreFilter

// ProcessAlertStore is an alias for alert.ProcessAlertStore.
type ProcessAlertStore = alert.ProcessAlertStore

// NewProcessAlertStore creates a new alert store with the given capacity.
func NewProcessAlertStore(maxSize int) *ProcessAlertStore {
	return alert.NewProcessAlertStore(maxSize)
}
