package proxy

import (
	"errors"
	"testing"
)

// The panel handler checked IsEnabled, then sent. send treats an unbound
// notifier as success (nil), so a session unbinding between the two acked a
// message "delivered" that went nowhere. A panel message sent with no overlay
// bound now reports that, and the handler acks it "logged".
func TestNotifyPanelMessage_UnboundIsNotDelivered(t *testing.T) {
	n := NewOverlayNotifier()
	err := n.NotifyPanelMessage("p1", &PanelMessage{Message: "hi"})
	if !errors.Is(err, ErrOverlayUnbound) {
		t.Fatalf("err = %v, want ErrOverlayUnbound", err)
	}
	n.SetEndpoint("")
	if err := n.NotifyPanelMessage("p1", &PanelMessage{Message: "hi"}); !errors.Is(err, ErrOverlayUnbound) {
		t.Fatalf("after clearing the endpoint: err = %v, want ErrOverlayUnbound", err)
	}
}
