package main

import (
	"os"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

var (
	discord               *discordgo.Session
	discordToken          string
	discordListenChannels []string
	discordLobbyChannel   string
	discordBotChannel     string
	discordBotID          string
)

func discordInit() {
	// Read some configuration values from environment variables
	// (they were loaded from the .env file in main.go)
	discordToken = os.Getenv("DISCORD_TOKEN")
	if len(discordToken) == 0 {
		log.Info("The \"DISCORD_TOKEN\" environment variable is blank; aborting Discord initalization.")
		return
	}
	discordListenChannelsString := os.Getenv("DISCORD_LISTEN_CHANNEL_IDS")
	if len(discordListenChannelsString) == 0 {
		log.Info("The \"DISCORD_LISTEN_CHANNEL_IDS\" environment variable is blank; aborting Discord initalization.")
		return
	}
	discordListenChannels = strings.Split(discordListenChannelsString, ",")
	discordLobbyChannel = os.Getenv("DISCORD_LOBBY_CHANNEL_ID")
	if len(discordLobbyChannel) == 0 {
		log.Info("The \"DISCORD_LOBBY_CHANNEL_ID\" environment variable is blank; aborting Discord initalization.")
		return
	}
	discordBotChannel = os.Getenv("DISCORD_BOT_CHANNEL_ID")
	if len(discordBotChannel) == 0 {
		log.Info("The \"DISCORD_BOT_CHANNEL_ID\" environment variable is blank; aborting Discord initalization.")
		return
	}

	go discordConnect()
}

func discordConnect() {
	// Bot accounts must be prefixed with "Bot"
	if v, err := discordgo.New("Bot " + discordToken); err != nil {
		log.Error("Failed to create a Discord session:", err)
		return
	} else {
		discord = v
	}

	// Register function handlers for various events
	discord.AddHandler(discordReady)
	discord.AddHandler(discordMessageCreate)

	// Open the websocket and begin listening
	if err := discord.Open(); err != nil {
		log.Error("Failed to open the Discord session:", err)
	}

	// Announce that the server has started
	msg := "The server has successfully started at: " + time.Now().Format("Mon Jan 02 15:04:05 MST 2006")
	d := &CommandData{
		Msg:    msg,
		Room:   "lobby",
		Server: true,
	}
	commandChat(nil, d)
}

/*
	Event handlers
*/

func discordReady(s *discordgo.Session, event *discordgo.Ready) {
	log.Info("Discord bot connected with username: " + event.User.Username)
	discordBotID = event.User.ID
}

// Copy messages from Discord to the lobby
func discordMessageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	// Ignore all messages created by the bot itself
	if m.Author.ID == discordBotID {
		return
	}

	// Only replicate messages from the listed channels
	if !stringInSlice(m.ChannelID, discordListenChannels) {
		return
	}

	// Get the channel
	var channel *discordgo.Channel
	if v, err := discord.Channel(m.ChannelID); err != nil {
		log.Error("Failed to get the Discord channel of \""+m.ChannelID+"\":", err)
	} else {
		channel = v
	}

	// Log the message
	log.Info("[D#" + channel.Name + "] <" + m.Author.Username + "#" + m.Author.Discriminator + "> " + m.Content)

	// Send everyone the notification
	commandMutex.Lock()
	defer commandMutex.Unlock()
	d := &CommandData{
		Username: m.Author.Username + "#" + m.Author.Discriminator,
		Msg:      m.Content,
		Discord:  true,
		Room:     "lobby",
	}
	commandChat(nil, d)
}

func discordSend(to string, username string, msg string) {
	if discord == nil {
		return
	}

	var fullMsg string
	if username != "" {
		// Text inside double asterisks are bolded
		fullMsg += "<**" + username + "**> "
	}
	fullMsg += msg

	if _, err := discord.ChannelMessageSend(to, fullMsg); err != nil {
		log.Error("Failed to send \""+fullMsg+"\" to Discord:", err)
	}
}
