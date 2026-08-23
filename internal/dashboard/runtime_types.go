package dashboard

import "time"

// RuntimeClient is the non-visual side of the dashboard. It owns the single
// camera stream and fans each frame out to preview, recording, and snapshots.
// Model.Update only sends small commands; camera and encoder work never runs on
// Bubble Tea's input loop.
type RuntimeClient interface {
	Start(ViewOptions)
	Previews() <-chan PreviewUpdate
	Events() <-chan RuntimeEvent
	UpdateView(ViewOptions)
	SelectCamera(string)
	StartRecording(RecordOptions)
	StopRecording()
	Snapshot(RecordOptions)
	OpenFolder()
	Save(Settings)
	Close() error
}

type ViewOptions struct {
	Version              uint64
	MaxColumns, MaxRows  int
	Fill, Mirror, Paused bool
	Symbol, Detail       string
	TargetFPS            int
	ColorLook            string
	LookStrength         int
}

type RecordOptions struct {
	Format, Quality, Symbol, Detail string
	Width, Height, FPS              int
	Fill                            bool
}

type PreviewUpdate struct {
	Version          uint64
	ANSI             string
	Columns, Rows    int
	Sequence         uint64
	SourceFPS        float64
	ShownFPS         float64
	Dropped          uint64
	RenderDuration   time.Duration
	CapturedAt       time.Time
	cameraGeneration uint64
	acknowledge      func(bool)
}

// acknowledgeRendered commits or rejects the canonical frame that produced
// this projection. The model calls it only after composing the matching view.
func (p PreviewUpdate) acknowledgeRendered(accepted bool) {
	if p.acknowledge != nil {
		p.acknowledge(accepted)
	}
}

type RuntimeEventKind int

const (
	RuntimeFindingCameras RuntimeEventKind = iota
	RuntimeDevicesFound
	RuntimeConnecting
	RuntimeCameraLive
	RuntimeCameraError
	RuntimeRecordingStarting
	RuntimeRecordingStarted
	RuntimeRecordingSaving
	RuntimeRecordingSaved
	RuntimeRecordingError
	RuntimeRecoveryStarting
	RuntimeRecoverySaved
	RuntimeRecoveryError
	RuntimeSnapshotSaving
	RuntimeSnapshotSaved
	RuntimeSnapshotError
	RuntimeFolderOpened
	RuntimeFolderError
	RuntimeSettingsSaved
	RuntimeSettingsError
	RuntimeLooksFound
)

type RuntimeEvent struct {
	Kind    RuntimeEventKind
	Devices []string
	Looks   []string
	Device  string
	Format  string
	Path    string
	Count   int
	Width   int
	Height  int
	FPS     float64
	Backend string
	Err     error
}
