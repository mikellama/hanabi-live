package main

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/Zamiell/hanabi-live/src/models"
)

func (g *Game) End() {
	g.DatetimeFinished = time.Now()

	// Send text messages showing how much time each player finished with
	// (even show them in non-timed games in case people are going for a speedrun)
	if g.Options.Timed {
		// Advance a turn so that we have an extra separator before the finishing times
		g.Actions = append(g.Actions, Action{
			Type: "turn",
			Num:  g.Turn,
			Who:  g.ActivePlayer,
		})
		// But don't notify the players; the finishing times will only appear in the replay

		for _, p := range g.Players {
			text := p.Name + " finished with a time of " + durationToString(p.Time)
			g.Actions = append(g.Actions, Action{
				Text: text,
			})
			// But don't notify the players; the finishing times will only appear in the replay
			log.Info(g.GetName() + text)
		}
	} else {
		// Get the total time
		var totalTime time.Duration
		for _, p := range g.Players {
			totalTime += p.Time
		}
		totalTime *= -1 // The duration will be negative since the clocks start at 0

		text := "The total game duration was: " + durationToString(totalTime)
		g.Actions = append(g.Actions, Action{
			Text: text,
		})
		// But don't notify the players; the finishing times will only appear in the replay
		log.Info(g.GetName() + text)
	}

	// Send the "gameOver" message
	loss := false
	if g.EndCondition > 1 {
		loss = true
	}
	g.Actions = append(g.Actions, Action{
		Type:  "gameOver",
		Score: g.Score,
		Loss:  loss,
	})
	g.NotifyAction()

	// Send everyone a clock message with an active value of null, which
	// will get rid of the timers on the client-side
	g.NotifyTime()

	// Send "reveal" messages to each player about the missing cards in their hand
	for _, p := range g.Players {
		for _, c := range p.Hand {
			type RevealMessage struct {
				Type  string `json:"type"`
				Which *Which `json:"which"`
			}
			p.Session.Emit("notify", &RevealMessage{
				Type: "reveal",
				Which: &Which{
					Rank:  c.Rank,
					Suit:  c.Suit,
					Order: c.Order,
				},
			})
		}
	}

	if g.EndCondition > 1 {
		g.Score = 0
	}

	// Log the game ending
	log.Info(g.GetName() + "Ended with a score of " + strconv.Itoa(g.Score) + ".")

	// Notify everyone that the table was deleted
	// (we will send a new table message later for the shared replay)
	notifyAllTableGone(g)

	// Record the game in the database
	row := models.GameRow{
		Name:            g.Name,
		Owner:           g.Owner,
		Variant:         g.Options.Variant,
		TimeBase:        int(g.Options.TimeBase),
		TimePerTurn:     g.Options.TimePerTurn,
		Seed:            g.Seed,
		Score:           g.Score,
		EndCondition:    g.EndCondition,
		DatetimeCreated: g.DatetimeCreated,
		DatetimeStarted: g.DatetimeStarted,
	}
	var databaseID int
	if v, err := db.Games.Insert(row); err != nil {
		log.Error("Failed to insert the game row:", err)
		return
	} else {
		databaseID = v
	}

	// Next, we have to insert rows for each of the participants
	for _, p := range g.Players {
		if err := db.GameParticipants.Insert(p.ID, databaseID, p.Notes); err != nil {
			log.Error("Failed to insert the game participant row:", err)
			return
		}
	}

	// Next, we have to insert rows for each of the actions
	for _, a := range g.Actions {
		var aString string
		if v, err := json.Marshal(a); err != nil {
			log.Error("Failed to convert the action to JSON:", err)
			return
		} else {
			aString = string(v)
		}

		if err := db.GameActions.Insert(databaseID, aString); err != nil {
			log.Error("Failed to insert the action row:", err)
			return
		}
	}

	var numSimilar int
	if v, err := db.Games.GetNumSimilar(g.Seed); err != nil {
		log.Error("Failed to get the number of games on seed "+g.Seed+":", err)
		return
	} else {
		numSimilar = v
	}

	// Send a "gameHistory" message to all the players in the game
	for _, p := range g.Players {
		var otherPlayerNames string
		for _, p2 := range g.Players {
			if p2.Name != p.Name {
				otherPlayerNames += p2.Name + ", "
			}
		}
		otherPlayerNames = strings.TrimSuffix(otherPlayerNames, ", ")

		h := make([]models.GameHistory, 0)
		h = append(h, models.GameHistory{
			ID:               databaseID,
			NumPlayers:       len(g.Players),
			NumSimilar:       numSimilar,
			Score:            g.Score,
			DatetimeFinished: g.DatetimeFinished,
			Variant:          g.Options.Variant,
			OtherPlayerNames: otherPlayerNames,
		})
		p.Session.NotifyGameHistory(h)
	}

	// Send a chat message with the game result and players
	announceGameResult(g, databaseID)

	log.Info("Finished database actions for the end of the game.")

	// Turn the game into a shared replay
	if _, ok := games[databaseID]; ok {
		log.Error("Failed to turn the game into a shared replay since there already exists a game with an ID of " + strconv.Itoa(databaseID) + ".")
		return
	}
	delete(games, g.ID)
	g.ID = databaseID
	games[g.ID] = g
	g.SharedReplay = true
	g.Name = "Shared replay for game #" + strconv.Itoa(g.ID)

	// Get the notes from all of the players
	notes := make([]models.PlayerNote, 0)
	for _, p := range g.Players {
		note := models.PlayerNote{
			ID:    p.ID,
			Name:  p.Name,
			Notes: p.Notes,
		}
		notes = append(notes, note)
	}

	// Turn the players into spectators
	for _, p := range g.Players {
		// Reset everyone's current game
		// (this is necessary for players who happen to be offline or in the lobby)
		p.Session.Set("currentGame", -1)

		// Skip offline players;
		// if they re-login, then they will just stay in the lobby
		if !p.Present {
			continue
		}

		g.Spectators[p.Session.UserID()] = p.Session
	}

	// Empty the players
	// (this is necessary so that "joined" is equal to false in the upcoming table message)
	g.Players = make([]*Player, 0)

	// End the shared replay if no-one is left
	if len(g.Spectators) == 0 {
		delete(games, g.ID)
		return
	}

	for _, s := range g.Spectators {
		// Reset everyone's status (both players and spectators are now spectators)
		s.Set("currentGame", g.ID)
		s.Set("status", "Shared Replay")
		notifyAllUser(s)

		// Activate the Replay Leader label
		s.NotifyReplayLeader(g)

		// Send them the notes from all players
		s.NotifyAllNotes(notes)
	}

	notifyAllTable(g)
	g.NotifyPlayerChange()
	g.NotifySpectators()
}

func announceGameResult(g *Game, databaseID int) {
	// Emote definitions
	pogChamp := "<:PogChamp:254683883033853954>"
	bibleThump := "<:BibleThump:254683882601840641>"

	// Make the list of names
	playerList := make([]string, 0)
	for _, p := range g.Players {
		playerList = append(playerList, p.Name)
	}
	msg := "[" + strings.Join(playerList, ", ") + "] "
	msg += "finished a " + strings.ToLower(variants[g.Options.Variant]) + " "
	msg += "game with a score of " + strconv.Itoa(g.Score) + ". "
	if g.Score == g.MaxScore() {
		msg += pogChamp + " "
	} else if g.Score == 0 {
		msg += bibleThump + " "
	}
	msg += "(#" + strconv.Itoa(databaseID) + " - " + g.Name + ")"

	d := &CommandData{
		Server: true,
		Msg:    msg,
		Room:   "lobby",
	}
	commandChat(nil, d)
}
