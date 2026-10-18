package domain

// Destination identifies an authenticated account on one server installation.
// Credentials may rotate without changing this identity.
type Destination struct {
	Server    string `json:"server"`
	ServerID  string `json:"server_id"`
	AccountID int64  `json:"account_id"`
}

func (d Destination) Bound() bool { return d.Server != "" && d.ServerID != "" && d.AccountID > 0 }

// Delivery is a revision of a local record awaiting acknowledgement.
type Delivery struct {
	ID          int64       `json:"id"`
	Revision    int64       `json:"revision"`
	Key         string      `json:"key"`
	Kind        string      `json:"kind"`
	State       string      `json:"state"`
	Destination Destination `json:"destination"`
	Match       *Match      `json:"match,omitempty"`
	Training    *Training   `json:"training,omitempty"`
}
