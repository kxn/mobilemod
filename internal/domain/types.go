package domain

import "time"

type Recipient struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type Identity struct {
	Module   string `json:"module"`
	Receiver string `json:"receiver"`
	SIM      string `json:"sim"`
}

type Metadata struct {
	Ref      int `json:"refNum"`
	Sequence int `json:"seqNum"`
	Total    int `json:"maxNum"`
	Year     int `json:"year"`
	Month    int `json:"mon"`
	Day      int `json:"day"`
	Hour     int `json:"hour"`
	Minute   int `json:"min"`
	Second   int `json:"sec"`
	Timezone int `json:"tz"`
}

type Envelope struct {
	Event           string          `json:"event"`
	ID              string          `json:"id,omitempty"`
	MessageID       int64           `json:"message_id,omitempty"`
	BootID          string          `json:"boot_id,omitempty"`
	DeviceID        string          `json:"device_id,omitempty"`
	From            string          `json:"from,omitempty"`
	Text            string          `json:"text,omitempty"`
	Metadata        Metadata        `json:"metadata,omitempty"`
	Number          string          `json:"number,omitempty"`
	ICCID           string          `json:"iccid,omitempty"`
	Core            string          `json:"core,omitempty"`
	Version         string          `json:"version,omitempty"`
	Protocol        int             `json:"protocol,omitempty"`
	Network         int             `json:"network,omitempty"`
	CSQ             int             `json:"csq,omitempty"`
	RSRP            int             `json:"rsrp,omitempty"`
	SMSReady        bool            `json:"sms_ready_event_seen,omitempty"`
	Submitted       *bool           `json:"submitted,omitempty"`
	State           string          `json:"state,omitempty"`
	Error           string          `json:"error,omitempty"`
	Durable         bool            `json:"durable,omitempty"`
	Drops           int             `json:"drops,omitempty"`
	SIMPresent      bool            `json:"sim_present,omitempty"`
	Capabilities    map[string]bool `json:"capabilities,omitempty"`
	Messages        []Envelope      `json:"messages,omitempty"`
	ContentLocation string          `json:"content_location,omitempty"`
	OccurredAt      int64           `json:"occurred_at,omitempty"`
}

type Message struct {
	ID         string    `json:"id"`
	Identity   Identity  `json:"identity"`
	From       string    `json:"from"`
	Text       string    `json:"text"`
	ReceivedAt time.Time `json:"received_at"`
	Kind       string    `json:"kind"`
	State      string    `json:"state"`
	Parts      int       `json:"parts"`
	Expected   int       `json:"expected"`
	Missing    []int     `json:"missing,omitempty"`
	TimeNote   string    `json:"time_note,omitempty"`
}

type Outbox struct {
	ID            string       `json:"id"`
	Recipient     Recipient    `json:"recipient"`
	Text          string       `json:"text"`
	CreatedAt     time.Time    `json:"created_at"`
	NextAttempt   time.Time    `json:"next_attempt"`
	Attempts      int          `json:"attempts"`
	SentAt        *time.Time   `json:"sent_at,omitempty"`
	LastCode      string       `json:"last_code,omitempty"`
	LastError     string       `json:"last_error,omitempty"`
	LastAttemptAt *time.Time   `json:"last_attempt_at,omitempty"`
	Forward       bool         `json:"forward,omitempty"`
	SourceID      string       `json:"source_id,omitempty"`
	ProviderID    string       `json:"provider_id,omitempty"`
	ChatID        string       `json:"chat_id,omitempty"`
	Reply         *ReplyTarget `json:"reply,omitempty"`
}

type Delivery struct {
	MessageID string `json:"message_id"`
	ChatID    string `json:"chat_id"`
}
type ReplyTarget struct {
	MessageID string    `json:"message_id"`
	SourceID  string    `json:"source_id"`
	ChatID    string    `json:"chat_id"`
	Receiver  string    `json:"receiver"`
	From      string    `json:"from"`
	SIM       string    `json:"sim"`
	Module    string    `json:"module,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type Incoming struct {
	EventID   string    `json:"event_id"`
	MessageID string    `json:"message_id"`
	ChatID    string    `json:"chat_id"`
	ChatType  string    `json:"chat_type"`
	Sender    string    `json:"sender"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
	ParentID  string    `json:"parent_id,omitempty"`
}

type CommandJob struct {
	Incoming  Incoming  `json:"incoming"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
}

type SendRecord struct {
	ID        string    `json:"id"`
	Module    string    `json:"module"`
	To        string    `json:"to"`
	Text      string    `json:"text"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Error     string    `json:"error,omitempty"`
	From      string    `json:"from,omitempty"`
	SIM       string    `json:"sim,omitempty"`
}
