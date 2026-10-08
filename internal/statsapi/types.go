package statsapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// Event names emitted by the Rocket League Stats API.
const (
	EvUpdateState         = "UpdateState"
	EvBallHit             = "BallHit"
	EvClockUpdatedSeconds = "ClockUpdatedSeconds"
	EvCountdownBegin      = "CountdownBegin"
	EvCrossbarHit         = "CrossbarHit"
	EvGoalReplayEnd       = "GoalReplayEnd"
	EvGoalReplayStart     = "GoalReplayStart"
	EvGoalReplayWillEnd   = "GoalReplayWillEnd"
	EvGoalScored          = "GoalScored"
	EvMatchCreated        = "MatchCreated"
	EvMatchInitialized    = "MatchInitialized"
	EvMatchDestroyed      = "MatchDestroyed"
	EvMatchEnded          = "MatchEnded"
	EvMatchPaused         = "MatchPaused"
	EvMatchUnpaused       = "MatchUnpaused"
	EvPodiumStart         = "PodiumStart"
	EvReplayCreated       = "ReplayCreated"
	EvRoundStarted        = "RoundStarted"
	EvStatfeedEvent       = "StatfeedEvent"
)

// Event is one decoded envelope. Data is always the JSON object bytes (an
// eventual JSON-encoded string has already been unwrapped).
type Event struct {
	Name string
	Data json.RawMessage
}

type envelope struct {
	Event Str             `json:"Event"`
	Data  json.RawMessage `json:"Data"`
}

// ErrNotEnvelope is returned when a JSON object has no Event field.
var ErrNotEnvelope = errors.New("statsapi: not an event envelope")

// DecodeEnvelope decodes `{"Event": "...", "Data": ...}` where Data may be an
// object or a JSON-encoded string containing an object.
func DecodeEnvelope(b []byte) (Event, error) {
	var env envelope
	if err := json.Unmarshal(b, &env); err != nil {
		return Event{}, fmt.Errorf("statsapi: bad envelope: %w", err)
	}
	if env.Event == "" {
		return Event{}, ErrNotEnvelope
	}
	data := bytes.TrimSpace(env.Data)
	// Unwrap string-encoded data, possibly several times (defensive).
	for i := 0; i < 3 && len(data) > 0 && data[0] == '"'; i++ {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			break
		}
		data = bytes.TrimSpace([]byte(s))
	}
	if len(data) == 0 || data[0] != '{' {
		data = []byte("{}")
	}
	return Event{Name: string(env.Event), Data: json.RawMessage(data)}, nil
}

// Decode decodes the event data into v leniently.
func (e Event) Decode(v any) error { return lenientUnmarshal(e.Data, v) }

// PlayerRef is the small {Name, Shortcut, TeamNum} reference used by many events.
type PlayerRef struct {
	Name     Str `json:"Name"`
	Shortcut Int `json:"Shortcut"`
	TeamNum  Int `json:"TeamNum"`
}

// Valid reports whether the reference names someone.
func (p *PlayerRef) Valid() bool { return p != nil && p.Name != "" }

// Player as found in UpdateState.Players.
type Player struct {
	Name       Str `json:"Name"`
	PrimaryID  Str `json:"PrimaryId"`
	Shortcut   Int `json:"Shortcut"`
	TeamNum    Int `json:"TeamNum"`
	Score      Int `json:"Score"`
	Goals      Int `json:"Goals"`
	Shots      Int `json:"Shots"`
	Assists    Int `json:"Assists"`
	Saves      Int `json:"Saves"`
	Touches    Int `json:"Touches"`
	CarTouches Int `json:"CarTouches"`
	Demos      Int `json:"Demos"`

	// Only present for the own team / spectator.
	HasCar       *Bool `json:"bHasCar"`
	Speed        *Num  `json:"Speed"`
	Boost        *Num  `json:"Boost"`
	Boosting     *Bool `json:"bBoosting"`
	OnGround     *Bool `json:"bOnGround"`
	OnWall       *Bool `json:"bOnWall"`
	Powersliding *Bool `json:"bPowersliding"`
	Demolished   *Bool `json:"bDemolished"`
	Supersonic   *Bool `json:"bSupersonic"`

	Attacker *PlayerRef `json:"Attacker"`
}

// HasSpectatorFields reports whether the detailed car state is present.
func (p *Player) HasSpectatorFields() bool {
	return p.HasCar != nil || p.Speed != nil || p.Boost != nil || p.OnGround != nil
}

type Team struct {
	Name           Str `json:"Name"`
	TeamNum        Int `json:"TeamNum"`
	Score          Int `json:"Score"`
	ColorPrimary   Str `json:"ColorPrimary"`
	ColorSecondary Str `json:"ColorSecondary"`
}

type Ball struct {
	Speed   Num `json:"Speed"`
	TeamNum Int `json:"TeamNum"`
}

type Game struct {
	Teams       []Team     `json:"Teams"`
	TimeSeconds *Int       `json:"TimeSeconds"`
	Overtime    Bool       `json:"bOvertime"`
	Frame       *Int       `json:"Frame"`
	Elapsed     *Num       `json:"Elapsed"`
	Ball        *Ball      `json:"Ball"`
	Replay      Bool       `json:"bReplay"`
	HasWinner   Bool       `json:"bHasWinner"`
	Winner      Str        `json:"Winner"`
	Arena       Str        `json:"Arena"`
	HasTarget   Bool       `json:"bHasTarget"`
	Target      *PlayerRef `json:"Target"`
}

type UpdateState struct {
	MatchGUID Str      `json:"MatchGuid"`
	Players   []Player `json:"Players"`
	Game      *Game    `json:"Game"`
}

type MatchGUIDOnly struct {
	MatchGUID Str `json:"MatchGuid"`
}

type ClockUpdated struct {
	MatchGUID   Str  `json:"MatchGuid"`
	TimeSeconds *Int `json:"TimeSeconds"`
	Overtime    Bool `json:"bOvertime"`
}

type MatchEnded struct {
	MatchGUID     Str  `json:"MatchGuid"`
	WinnerTeamNum *Int `json:"WinnerTeamNum"`
}

type Vec3 struct {
	X Num `json:"X"`
	Y Num `json:"Y"`
	Z Num `json:"Z"`
}

type BallLastTouch struct {
	Player *PlayerRef `json:"Player"`
	Speed  Num        `json:"Speed"`
}

type GoalScored struct {
	MatchGUID      Str            `json:"MatchGuid"`
	GoalSpeed      Num            `json:"GoalSpeed"`
	GoalTime       Num            `json:"GoalTime"`
	ImpactLocation *Vec3          `json:"ImpactLocation"`
	Scorer         *PlayerRef     `json:"Scorer"`
	Assister       *PlayerRef     `json:"Assister"`
	BallLastTouch  *BallLastTouch `json:"BallLastTouch"`
}

type StatfeedEvent struct {
	MatchGUID       Str        `json:"MatchGuid"`
	EventName       Str        `json:"EventName"`
	Type            Str        `json:"Type"`
	MainTarget      *PlayerRef `json:"MainTarget"`
	SecondaryTarget *PlayerRef `json:"SecondaryTarget"`
}

type BallHitBall struct {
	PreHitSpeed  Num   `json:"PreHitSpeed"`
	PostHitSpeed Num   `json:"PostHitSpeed"`
	Location     *Vec3 `json:"Location"`
}

type BallHit struct {
	MatchGUID Str          `json:"MatchGuid"`
	Players   PlayerRefs   `json:"Players"`
	Player    *PlayerRef   `json:"Player"`
	Ball      *BallHitBall `json:"Ball"`
}

// PlayerRefs accepts either an array of PlayerRef or a single object.
type PlayerRefs []PlayerRef

func (p *PlayerRefs) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	*p = nil
	if len(b) == 0 {
		return nil
	}
	switch b[0] {
	case '[':
		var raws []json.RawMessage
		if json.Unmarshal(b, &raws) != nil {
			return nil
		}
		for _, r := range raws {
			var one PlayerRef
			if lenientUnmarshal(r, &one) == nil {
				*p = append(*p, one)
			}
		}
	case '{':
		var one PlayerRef
		if lenientUnmarshal(b, &one) == nil {
			*p = append(*p, one)
		}
	}
	return nil
}
