// Package events reports what happened to a service's resources to Kayra's
// events.<domain>, as Kubernetes' EventRecorder does: Recorder queues each
// event and sends them in batches, merges the same event seen again into a
// series, and limits how many one object gets. It never blocks the caller
// and never fails it: events are best effort. server.WithEvents records
// one for every write the service serves.
package events

import (
	"time"
)

// The types of an event.
const (
	Normal  = "Normal"
	Warning = "Warning"
)

// Regarding is the object an event is about.
type Regarding struct {
	API       string `json:"api"`
	Namespace string `json:"namespace,omitempty"`
	Resource  string `json:"resource"`
	Name      string `json:"name,omitempty"`
}

// Event is one event, as Kayra's events.<domain> takes it. Kayra sets its
// id, received_at and reporter.
type Event struct {
	Regarding           Regarding      `json:"regarding"`
	Type                string         `json:"type"`
	Reason              string         `json:"reason"`
	Note                string         `json:"note,omitempty"`
	Action              string         `json:"action,omitempty"`
	ReportingController string         `json:"reporting_controller"`
	User                string         `json:"user,omitempty"`
	RequestID           string         `json:"request_id,omitempty"`
	Details             map[string]any `json:"details,omitempty"`
	Time                time.Time      `json:"time"`
	Series              *Series        `json:"series,omitempty"`
}

// Series is the same event seen again: how often, and when last.
type Series struct {
	Count        int       `json:"count"`
	LastObserved time.Time `json:"last_observed"`
}

// key is what makes two events the same, for a series: the object, type,
// reason, controller and note.
func (e Event) key() string {
	r := e.Regarding
	return r.API + "\x00" + r.Namespace + "\x00" + r.Resource + "\x00" + r.Name + "\x00" + e.Type + "\x00" + e.Reason + "\x00" + e.ReportingController + "\x00" + e.Note
}

// object is the object it is about, for the spam limit.
func (e Event) object() string {
	r := e.Regarding
	return r.API + "\x00" + r.Namespace + "\x00" + r.Resource + "\x00" + r.Name
}
