package domain

// Observation is a game fact after tolerant transport decoding. Name selects the
// corresponding payload; absent payloads are ignored. Consumers must not mutate
// payloads after handing them to the tracker.
type Observation struct {
	Name      string
	SpeedUnit SpeedUnit
	MatchGUID string
	State     *StateObservation
	Clock     *ClockObservation
	Goal      *GoalObservation
	Feed      *FeedObservation
	Hit       *HitObservation
	End       *EndObservation
}

const (
	EvUpdateState         = "UpdateState"
	EvBallHit             = "BallHit"
	EvClockUpdatedSeconds = "ClockUpdatedSeconds"
	EvCountdownBegin      = "CountdownBegin"
	EvGoalReplayEnd       = "GoalReplayEnd"
	EvGoalReplayStart     = "GoalReplayStart"
	EvGoalScored          = "GoalScored"
	EvMatchCreated        = "MatchCreated"
	EvMatchInitialized    = "MatchInitialized"
	EvMatchDestroyed      = "MatchDestroyed"
	EvMatchEnded          = "MatchEnded"
	EvMatchPaused         = "MatchPaused"
	EvMatchUnpaused       = "MatchUnpaused"
	EvRoundStarted        = "RoundStarted"
	EvStatfeedEvent       = "StatfeedEvent"
)

type PlayerRef struct {
	Name              string
	Shortcut, TeamNum int
}

func (p *PlayerRef) Valid() bool { return p != nil && p.Name != "" }

// ObservedPlayer preserves absent telemetry separately from measured zeros.
type ObservedPlayer struct {
	Name, PrimaryID                                                          string
	Shortcut, TeamNum, Score, Goals, Shots, Assists, Saves, Touches, Demos   int
	HasCar, Boosting, OnGround, OnWall, Powersliding, Demolished, Supersonic *bool
	Speed, Boost                                                             *float64
}

// HasSpectatorFields keeps the established identity heuristic. Movement has
// stricter eligibility rules and also accepts an observed demolition alone.
func (p *ObservedPlayer) HasSpectatorFields() bool {
	return p.HasCar != nil || p.Speed != nil || p.Boost != nil || p.OnGround != nil
}

type TeamScore struct{ TeamNum, Score int }
type ObservedGame struct {
	Teams                       []TeamScore
	TimeSeconds                 *int
	Overtime, Replay, HasTarget bool
	Arena                       string
	Target                      *PlayerRef
}
type StateObservation struct {
	MatchGUID string
	Players   []ObservedPlayer
	Game      *ObservedGame
}
type ClockObservation struct {
	MatchGUID   string
	TimeSeconds *int
	Overtime    bool
}
type EndObservation struct {
	MatchGUID     string
	WinnerTeamNum *int
}
type Position struct{ X, Y, Z float64 }
type GoalObservation struct {
	MatchGUID        string
	GoalSpeed        float64
	ImpactLocation   *Position
	Scorer, Assister *PlayerRef
	LastTouch        *PlayerRef
}
type FeedObservation struct {
	MatchGUID, EventName        string
	MainTarget, SecondaryTarget *PlayerRef
}
type ObservedHitBall struct {
	PreHitSpeed, PostHitSpeed float64
	Location                  *Position
}
type HitObservation struct {
	MatchGUID string
	Players   []PlayerRef
	Player    *PlayerRef
	Ball      *ObservedHitBall
}
