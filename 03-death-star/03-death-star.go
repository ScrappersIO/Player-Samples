package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"io"
	"log"
	"math"
	"net"
	"time"
)

const (
	MaxHealth int     = 12
	MaxPow    int     = 12
	BotDiam   float64 = 60
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

	// DEATH STAR: Improved Death Dish
	// - Arrange in a satellite dish shape.
	// - Point dish at center of enemy swarm.
	// - Keep minimum distance away from center of enemy swarm.
	// - Focus fire on closest enemy.
	// - If a bot is in position, power should be mostly fire and shield.
	// - If a bot is out of position, divert fire power to movement.

	var myBots, theirBots []*GDBBot
	var keepDist float64 = BotDiam * 20
	const HurryDist float64 = BotDiam * 3
	const FireDist float64 = BotDiam / 2

	for { // Loop indefinitely

		theirBots = gdb.TheirBots()
		if len(theirBots) == 0 {
			return
		}

		// Determine center of enemy swarm
		var x, y int
		for _, bot := range theirBots {
			x += bot.X
			y += bot.Y
		}
		x = x / len(theirBots)
		y = y / len(theirBots)

		myBots = gdb.MyBots()
		if len(myBots) == 0 {
			return
		}

		// This is the pivot point of our satellite dish
		centerIndex := len(myBots) / 2
		centerBot := myBots[centerIndex]

		// Determine angle of separation required to
		// space my bots out shoulder to shoulder at
		// the distance from target.
		circumference := 2 * math.Pi * keepDist
		segments := circumference / BotDiam
		radians := (2 * math.Pi) / segments

		// Find nearest enemy
		closeDist := math.MaxFloat64
		var closeBot *GDBBot
		for _, bot := range theirBots {
			dist := distance(centerBot.X, centerBot.Y, bot.X, bot.Y)
			if dist < closeDist {
				closeDist = dist
				closeBot = bot
			}
		}

		// Postion bots and target
		for i, bot := range myBots {

			// Determine existing angle between enemy
			// swarm and center bot.
			angle := coordAngle(x, y, centerBot.X, centerBot.Y)

			// Adjust angle for this bot's position in line
			angle += radians * float64(i-centerIndex)

			// Calculate position based on this angle and
			// the desired distance.
			newX := int(math.Cos(angle)*keepDist) + x
			newY := int(math.Sin(angle)*keepDist) + y

			// Move and Target
			send(bot.Move(newX, newY))
			send(bot.Target(closeBot))

			// Determine power
			distToPosition := distance(newX, newY, bot.X, bot.Y)
			if distToPosition > HurryDist {
				send(bot.Power(0, 7, 5))
			} else if distToPosition <= FireDist {
				send(bot.Power(5, 2, 5))
			}

		}

		// Sleep for 100ms
		time.Sleep(time.Second / 10)
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

	// We want to follow at a respectable distance,
	// so we calculate a new x,y.
	angle := botAngle(bot, b)
	x := int(math.Cos(angle)*BotDiam) + bot.X
	y := int(math.Sin(angle)*BotDiam) + bot.Y
	return b.Move(x, y)
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

// Angle returns the angle in radians of
// the line from bot1 to bot2.
func botAngle(bot1, bot2 *GDBBot) float64 {
	xDelt := float64(bot2.X - bot1.X)
	yDelt := float64(bot2.Y - bot1.Y)
	return math.Atan2(yDelt, xDelt)
}

func coordAngle(x1, y1, x2, y2 int) float64 {
	xDelt := float64(x2 - x1)
	yDelt := float64(y2 - y1)
	return math.Atan2(yDelt, xDelt)
}
