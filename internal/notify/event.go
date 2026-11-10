package notify

import "time"

// Kind is what happened.
type Kind string

// Event kinds. The arr event types they become: Grab, Download (Download with isUpgrade for Upgrade), Rename,
// MovieDelete / SeriesDelete, MovieFileDelete / EpisodeFileDelete, Health / HealthRestored, SeriesAdd / MovieAdded,
// ManualInteractionRequired, Test.
const (
	Grab                      Kind = "grab"
	Download                  Kind = "download"
	Upgrade                   Kind = "upgrade"
	Rename                    Kind = "rename"
	Delete                    Kind = "delete"      // a view removed a title's versions
	FileDelete                Kind = "file_delete" // a file was unlinked
	Health                    Kind = "health"
	HealthRestored            Kind = "health_restored"
	SeriesAdd                 Kind = "series_add"
	MovieAdded                Kind = "movie_added"
	ManualInteractionRequired Kind = "manual_interaction_required"
	Test                      Kind = "test"
	RequestPending            Kind = "request_pending"
	RequestApproved           Kind = "request_approved"
	RequestAutoApproved       Kind = "request_auto_approved"
	RequestDeclined           Kind = "request_declined"
	RequestAvailable          Kind = "request_available"
	RequestPartial            Kind = "request_partial_available"
	RequestFailed             Kind = "request_failed"
	// Error is a new error-inbox group, or one that came back after it was resolved (Discord and ntfy targets only).
	Error Kind = "error"
	// Digest is the daily summary (Discord and ntfy targets only).
	Digest Kind = "digest"
)

// PolicyEvent is the policy's name for a kind (notify.targets[*].events): file deletes count as delete.
func (k Kind) PolicyEvent() string {
	switch k {
	case FileDelete:
		return "delete"
	case RequestAvailable, RequestPartial:
		return "request_ready"
	}
	return string(k)
}

// Event is one thing a webhook announces. Which fields are set depends on the kind.
type Event struct {
	Kind Kind
	At   time.Time
	// View is the view the event came through (deletes, tests); "" for pipeline events, which go to every view
	// holding Version.
	View    string
	TitleID int64
	Version string
	// DownloadID is our download (grab, download, upgrade).
	DownloadID int64
	// Episodes are the episode ids involved (series).
	Episodes []int64
	// File is the file a FileDelete removed (or a Download linked).
	File *File
	// RenamedFiles are moved library files; paths follow the same privacy gate as File.
	RenamedFiles []RenamedFile
	// Reason says why (deletes): manual, upgrade, ...
	Reason string
	// Health is the changed check (Health), or the previous check when it cleared (HealthRestored).
	Health []HealthItem
	// HealthWas is the check before a Health event changed its level (error to warning); nil for a new problem.
	HealthWas *HealthItem
	// NotificationID limits a Test to one installed notification; 0 for policy targets.
	NotificationID int64
	// Target limits a Test to one policy target.
	Target string
	// Request carries the request details for request notifications.
	Request *Request
	// Error is the inbox group an Error event announces.
	Error *ErrorItem
	// Digest is the Digest event's summary.
	Digest *DigestSummary
}

// ErrorItem is an error-inbox group as an alert names it.
type ErrorItem struct {
	ID        int64
	Source    string
	Component string
	// Sample is the latest occurrence, redacted by the inbox.
	Sample string
	Count  int64
	// Reopened: it was resolved and happened again.
	Reopened bool
}

// Request is the request data included in native request notifications.
type Request struct {
	ID       int64            `json:"id"`
	Name     string           `json:"name"`
	Kind     string           `json:"kind"`
	TitleID  int64            `json:"titleId,omitempty"`
	TMDbID   int64            `json:"tmdbId"`
	TVDbID   int64            `json:"tvdbId"`
	Image    string           `json:"image"`
	Reason   string           `json:"reason,omitempty"`
	Status   string           `json:"status"`
	Previous string           `json:"previous,omitempty"`
	Change   string           `json:"change"`
	Versions []RequestVersion `json:"versions"`
	Seasons  []int            `json:"seasons"`
	Users    []RequestUser    `json:"requesters"`
}

// RequestVersion identifies a requested copy.
type RequestVersion struct {
	Key       string `json:"key"`
	Label     string `json:"label"`
	Available bool   `json:"available"`
}

// RequestUser is a requester named in a request notification.
type RequestUser struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	// DiscordID is the Discord user an admin set on the account; only a Discord message uses it (an @mention).
	DiscordID string `json:"-"`
}

// File is a library file.
type File struct {
	ID             int64
	Path           string
	RelativePath   string
	Size           int64
	Quality        string
	QualityVersion int
	SceneName      string
	ReleaseGroup   string
}

// RenamedFile is a moved file and its previous paths.
type RenamedFile struct {
	File                 File
	PreviousPath         string
	PreviousRelativePath string
}

// HealthItem is one health message.
type HealthItem struct {
	WikiURL string `json:"wikiUrl"`
	Source  string `json:"source"`
	Type    string `json:"type"`
	Message string `json:"message"`
}

// Payload is one arr-shaped body for one view.
type Payload struct {
	View string
	// EventType is the arr's (Grab, Download, ...).
	EventType string
	// Flag is the NotificationResource switch that selects it (onGrab, onDownload, onUpgrade, onRename,
	// onMovieDelete, onSeriesDelete, onMovieFileDelete, onEpisodeFileDelete, onHealthIssue, onHealthRestored,
	// onSeriesAdd, onMovieAdded, onManualInteractionRequired; "" for Test).
	Flag string
	Body any
}
