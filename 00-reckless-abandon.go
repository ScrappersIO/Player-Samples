package main

import (
	"io"
	"log"
	"net"
	"flag"
	"math"
	"time"
	"bufio"
	"math/rand"
	"encoding/json"
)

const (
	MaxHealth int = 12
)

var (
	// TCP connection to game.
	gameConn net.Conn
	// Queue of incoming messages
	msgQueue chan MsgQueueItem
)

// MsgQueueItem is a simple vehicle for TCP
// data on the incoming message queue.
type MsgQueueItem struct {
	Msg string
	Err error
}

// Command contains all the fields that a player might
// pass as part of a command. Fill in the fields that
// matter, then marshal into JSON and send.
type Command struct {
	Cmd  string
	BID  int
	X    int
	Y    int
	TPID int
	TBID int
	FPow int
	MPow int
	SPow int
}

// Msg is used to unmarshal every message in order
// to check what type of message it is.
type Msg struct {
	Type string
}

// BotMsg is used to unmarshal a BOT representation
// sent from the game.
type BotMsg struct {
	PID, BID   int
	X, Y       int
	Health     int
	Fired      bool
	HitX, HitY int
	Scrap      int
	Shield     bool
}

// ReadyMsg is used to unmarshal the READY
// message sent from the game.
type ReadyMsg struct {
	PID  int
	Bots []BotMsg
}

// Create our game data storage location
var gdb GameDatabase

func main() {

	var err error
	gdb = GameDatabase{}
	msgQueue = make(chan MsgQueueItem, 1200)

	// What port should we connect to?
	var port string
	flag.StringVar(&port, "port", "50000", "Port that Scrappers game is listening on.")
	flag.Parse()

	// Connect to the game
	gameConn, err = net.Dial("tcp", ":"+port)
	if err != nil {
		log.Fatalf("Failed to connect to game: %v\n", err)
	}
	defer gameConn.Close()

	// Process messages off the incoming message queue
	go processMsgs()

	// Listen for message from the game, exit if connection
	// closes, add message to message queue.
	reader := bufio.NewReader(gameConn)
	for {
		msg, err := reader.ReadString('\n')
		if err == io.EOF {
			log.Println("Game over (connection closed).")
			return
		}
		msgQueue <- MsgQueueItem{msg, err}
	}
}

func runStrategy() {

	// RECKLESS ABANDON
	// - For three seconds, all bots move as fast as possible in a random direction.
	// - After three seconds, split power between speed and firepower.
	// - Every 250ms...
	//     - Identify the enemy bot with the lowest health.
	//     - If there's a tie, pick the one closest to the group.
	//     - Everybody moves towards and targets the bot.

	var myBots, theirBots []*GDBBot

	// To get some variance in the shot tick, add some
	// wait time between the transition to fighting.
	firstTime := true

	// Move quickly in random direction.
	// Also, might as well get a shield.
	myBots = gdb.MyBots()
	for _, bot := range myBots {
		send(bot.Power(0, 11, 1))
		radians := 2.0*math.Pi*rand.Float64()
		x := bot.X + int(math.Cos(radians)*999)
		y := bot.Y + int(math.Sin(radians)*999)
		send(bot.Move(x, y))
	}

	// Wait three seconds
	time.Sleep(3*time.Second)

	// Split power between speed and fire
	for _, bot := range myBots {
		send(bot.Power(6, 6, 0))
	}

	for { // Loop indefinitely

		var target *GDBBot

		// Calculate the lowest health out there
		lowHealth := MaxHealth
		theirBots = gdb.TheirBots() // Refresh enemy list
		for _, bot := range theirBots {
			if bot.Health < lowHealth {
				lowHealth = bot.Health
			}
		}

		// Find the weakest enemy bots
		weakBots := make([]*GDBBot, 0, len(theirBots))
		for _, bot := range theirBots {
			if bot.Health == lowHealth {
				weakBots = append(weakBots, bot)
			}
		}

		// If there are no weak bots, the game should be
		// over, so let's quit the strategy loop.
		if len(weakBots) == 0 {
			return
		}

		// If there's more than one weak bot, find the one that's
		// closest to the average position of our bots.
		if len(weakBots) > 1 {

			// Calculate the average position of the swarm.
			ttlX, ttlY := 0, 0
			myBots = gdb.MyBots() // Refresh friendly bot list
			for _, bot := range myBots {
				ttlX += bot.X
				ttlY += bot.Y
			}
			avgX := ttlX / len(myBots)
			avgY := ttlY / len(myBots)

			// Find the closest weak bot
			closeBot := weakBots[0]
			closeDist := distance(avgX, avgY, closeBot.X, closeBot.Y)
			for _, bot := range weakBots {
				dist := distance(avgX, avgY, closeBot.X, closeBot.Y)
				if dist < closeDist {
					closeDist = dist
					closeBot = bot
				}
			}

			// We have our target!
			target = closeBot

		// If there is only one weak bot, it is our target.
		} else {
			target = weakBots[0]
		}

		// Move towards and target
		myBots = gdb.MyBots() // Refresh friendly bot list
		for _, bot := range myBots {
			send(bot.Follow(target))
			send(bot.Target(target))
			if firstTime { time.Sleep(time.Second/10) }
		}

		// Sleep for 250ms
		time.Sleep(time.Second/4)

		firstTime = false
	}
}

func processMsgs() {

	for {
		queueItem := <-msgQueue
		jsonmsg := queueItem.Msg
		err := queueItem.Err

		if err != nil {
			log.Printf("Unknown error reading from connection: %v", err)
			continue
		}

		// Determine the type of message first
		var msg Msg
		err = json.Unmarshal([]byte(jsonmsg), &msg)
		if err != nil {
			log.Printf("Failed to marshal json message %v: %v\n", jsonmsg, err)
			return
		}

		// Handle the message type

		// The READY message should be the first we get. We
		// process all the data, then kick off our strategy.
		if msg.Type == "READY" {

			// Unmarshal the data
			var ready ReadyMsg
			err = json.Unmarshal([]byte(jsonmsg), &ready)
			if err != nil {
				log.Printf("Failed to marshal json message %v: %v\n", jsonmsg, err)
			}

			// Save our player ID
			gdb.PID = ready.PID
			log.Printf("My player ID is %v.\n", gdb.PID)

			// Save the bots
			for _, bot := range ready.Bots {
				gdb.InsertUpdateBot(bot)
			}

			// Kick off our strategy
			go runStrategy()

			continue
		}

		// The BOT message is sent when something about a bot changes.
		if msg.Type == "BOT" {

			// Unmarshal the data
			var bot BotMsg
			err = json.Unmarshal([]byte(jsonmsg), &bot)
			if err != nil {
				log.Printf("Failed to marshal json message %v: %v\n", jsonmsg, err)
			}

			// Update or add the bot
			gdb.InsertUpdateBot(bot)

			continue
		}

		// If we've gotten to this point, then we
		// were sent a message we don't understand.
		log.Printf("Recieved unknown message type \"%v\".", msg.Type)
	}
}

///////////////////
// GAME DATABASE //
///////////////////

// GameDatabase stores all the data
// sent to us by the game.
type GameDatabase struct {
	Bots []GDBBot
	PID  int
}

// GDBBot is the Bot struct for the Game Database.
type GDBBot struct {
	BID, PID int
	X, Y     int
	Health   int
}

// InserUpdateBot either updates a bot's info,
// deletes a dead bot, or adds a new bot.
func (gdb *GameDatabase) InsertUpdateBot(b BotMsg) {

	// If this is a dead bot, remove and ignore
	if b.Health <= 0 {

		for i := 0; i < len(gdb.Bots); i++ {
			if gdb.Bots[i].BID == b.BID && gdb.Bots[i].PID == b.PID {
				gdb.Bots = append(gdb.Bots[:i], gdb.Bots[i+1:]...)
				return
			}
		}
		return
	}

	// Otherwise, update...
	for i, bot := range gdb.Bots {
		if b.BID == bot.BID && b.PID == bot.PID {
			gdb.Bots[i].X = b.X
			gdb.Bots[i].Y = b.Y
			gdb.Bots[i].Health = b.Health
			return
		}
	}

	// ... or Add
	bot := GDBBot{}
	bot.PID = b.PID
	bot.BID = b.BID
	bot.X = b.X
	bot.Y = b.Y
	bot.Health = b.Health
	gdb.Bots = append(gdb.Bots, bot)
}

// MyBots returns a pointer array of GDBBots owned by us.
func (gdb *GameDatabase) MyBots() []*GDBBot {
	bots := make([]*GDBBot, 0)
	for i, bot := range gdb.Bots {
		if bot.PID == gdb.PID {
			bots = append(bots, &gdb.Bots[i])
		}
	}
	return bots
}


// TheirBots returns a pointer array of GDBBots NOT owned by us.
func (gdb *GameDatabase) TheirBots() []*GDBBot {
	bots := make([]*GDBBot, 0)
	for i, bot := range gdb.Bots {
		if bot.PID != gdb.PID {
			bots = append(bots, &gdb.Bots[i])
		}
	}
	return bots
}

// Move returns a command struct for movement.
func (b *GDBBot) Move(x, y int) Command {
	cmd := Command{}
	cmd.Cmd = "MOVE"
	cmd.BID = b.BID
	cmd.X = x
	cmd.Y = y
	return cmd
}

// Follow is a convenience function which returns a
// command stuct for movement using a bot as a destination.
func (b *GDBBot) Follow(bot *GDBBot) Command {
	cmd := Command{}
	cmd.Cmd = "MOVE"
	cmd.BID = b.BID
	cmd.X = bot.X
	cmd.Y = bot.Y
	return cmd
}

// Target returns a command struct for targeting a bot.
func (b *GDBBot) Target(bot *GDBBot) Command {
	cmd := Command{}
	cmd.Cmd = "TARGET"
	cmd.BID = b.BID
	cmd.TPID = bot.PID
	cmd.TBID = bot.BID
	return cmd
}

// Power returns a command struct for seting the power of a bot.
func (b *GDBBot) Power(fire, move, shield int) Command {
	cmd := Command{}
	cmd.Cmd = "POWER"
	cmd.BID = b.BID
	cmd.FPow = fire
	cmd.MPow = move
	cmd.SPow = shield
	return cmd
}

////////////////////
// MISC FUNCTIONS //
////////////////////

// Send marshals a command to JSON and sends to the game.
func send(cmd Command) {
	bytes, err := json.Marshal(cmd)
	if err != nil {
		log.Fatalf("Failed to mashal command into JSON: %v\n", err)
	}
	bytes = append(bytes, []byte("\n")...)
	gameConn.Write(bytes)
}

// Distance calculates the distance between two points.
func distance(xa, ya, xb, yb int) float64 {
	xdist := float64(xb - xa)
	ydist := float64(yb - ya)
	return math.Sqrt(math.Pow(xdist, 2) + math.Pow(ydist, 2))
}
