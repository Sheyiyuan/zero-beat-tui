package sysmedia

import shared "github.com/sheyiyuan/zero-beat-core/sysmedia"

type LoopStatus = shared.LoopStatus
type Snapshot = shared.Snapshot
type CommandKind = shared.CommandKind
type Command = shared.Command
type Service = shared.Service

var New = shared.New

const (
	LoopNone             = shared.LoopNone
	LoopTrack            = shared.LoopTrack
	LoopPlaylist         = shared.LoopPlaylist
	CommandToggle        = shared.CommandToggle
	CommandPlay          = shared.CommandPlay
	CommandPause         = shared.CommandPause
	CommandNext          = shared.CommandNext
	CommandPrevious      = shared.CommandPrevious
	CommandStop          = shared.CommandStop
	CommandSeekTo        = shared.CommandSeekTo
	CommandSeekBy        = shared.CommandSeekBy
	CommandSetVolume     = shared.CommandSetVolume
	CommandSetShuffle    = shared.CommandSetShuffle
	CommandSetLoopStatus = shared.CommandSetLoopStatus
)
